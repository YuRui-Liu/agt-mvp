package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const MockOTP = "246810"

const (
	mockChallengeCapacity = 1024
	mockChallengeTTL      = 5 * time.Minute
)

type mockChallenge struct {
	expiresAt time.Time
}

type MockOptions struct {
	Scenario      string
	AnalysisDelay time.Duration
	Now           func() time.Time
	Rand          io.Reader
}

type mockUpload struct {
	owner    string
	metadata UploadMetadata
	target   UploadTarget
	body     []byte
	uploaded bool
	task     Task
}

type MockClient struct {
	mu         sync.Mutex
	now        func() time.Time
	scenario   string
	delay      time.Duration
	rand       io.Reader
	challenges map[[sha256.Size]byte]mockChallenge
	sessions   map[string]AuthSession
	consents   map[string]string
	uploads    map[string]*mockUpload
	byKey      map[string]string
}

func NewMockClient(options MockOptions) *MockClient {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	scenario := options.Scenario
	if scenario == "" {
		scenario = "success"
	}
	delay := options.AnalysisDelay
	if delay <= 0 {
		delay = 30 * time.Second
	}
	random := options.Rand
	if random == nil {
		random = rand.Reader
	}
	return &MockClient{now: now, scenario: scenario, delay: delay, challenges: map[[sha256.Size]byte]mockChallenge{},
		rand: random, sessions: map[string]AuthSession{}, consents: map[string]string{}, uploads: map[string]*mockUpload{}, byKey: map[string]string{}}
}

func (m *MockClient) RequestOTP(ctx context.Context, phone string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizePhone(phone)
	if err != nil {
		return ErrInvalid
	}
	key := sha256.Sum256([]byte(normalized))
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupChallengesLocked(m.now())
	if _, exists := m.challenges[key]; !exists && len(m.challenges) >= mockChallengeCapacity {
		return ErrCapacity
	}
	m.challenges[key] = mockChallenge{expiresAt: m.now().Add(mockChallengeTTL)}
	return nil
}

func (m *MockClient) VerifyOTP(ctx context.Context, phone, otp string) (AuthSession, error) {
	if err := ctx.Err(); err != nil {
		return AuthSession{}, err
	}
	normalized, err := normalizePhone(phone)
	if err != nil {
		return AuthSession{}, ErrUnauthenticated
	}
	key := sha256.Sum256([]byte(normalized))
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	challenge, exists := m.challenges[key]
	delete(m.challenges, key)
	m.cleanupChallengesLocked(now)
	if !exists || !now.Before(challenge.expiresAt) || otp != MockOTP || m.scenario == "otp_error" {
		return AuthSession{}, ErrUnauthenticated
	}
	subject := fmt.Sprintf("subject-%x", sha256.Sum256([]byte(normalized)))[:24]
	bearer, err := randomToken(m.rand)
	if err != nil {
		return AuthSession{}, fmt.Errorf("generate auth session: %w", err)
	}
	auth := AuthSession{Identity: Identity{SubjectID: subject, KuAIID: "KUAI-" + subject[len(subject)-8:]},
		Bearer: bearer, ExpiresAt: m.now().Add(15 * time.Minute)}
	m.sessions[auth.Bearer] = auth
	return auth, nil
}

func (m *MockClient) cleanupChallengesLocked(now time.Time) {
	for key, challenge := range m.challenges {
		if !now.Before(challenge.expiresAt) {
			delete(m.challenges, key)
		}
	}
}

func normalizePhone(phone string) (string, error) {
	value := strings.TrimSpace(phone)
	if len(value) < 8 || len(value) > 20 {
		return "", ErrInvalid
	}
	for index, r := range value {
		if r == '+' && index == 0 {
			continue
		}
		if r < '0' || r > '9' {
			return "", ErrInvalid
		}
	}
	return value, nil
}

func (m *MockClient) SubmitConsent(ctx context.Context, auth AuthSession, consent Consent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.authenticate(ctx, auth); err != nil {
		return err
	}
	if consent.Version != ConsentVersion {
		return ErrConsentRequired
	}
	m.consents[auth.Bearer] = consent.Version
	return nil
}

func (m *MockClient) CreateUpload(ctx context.Context, auth AuthSession, metadata UploadMetadata) (UploadTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.authenticate(ctx, auth); err != nil {
		return UploadTarget{}, err
	}
	if m.consents[auth.Bearer] != ConsentVersion {
		return UploadTarget{}, ErrConsentRequired
	}
	if !validMetadata(metadata) {
		return UploadTarget{}, ErrInvalid
	}
	key := auth.Identity.SubjectID + "\x00" + metadata.IdempotencyKey
	if id := m.byKey[key]; id != "" {
		u := m.uploads[id]
		if u.metadata != metadata {
			return UploadTarget{}, ErrConflict
		}
		return cloneTarget(u.target), nil
	}
	id, err := randomToken(m.rand)
	if err != nil {
		return UploadTarget{}, fmt.Errorf("generate upload id: %w", err)
	}
	target := UploadTarget{TaskID: id, URL: "mock://upload/" + id, Method: "PUT"}
	m.byKey[key], m.uploads[id] = id, &mockUpload{owner: auth.Identity.SubjectID, metadata: metadata, target: target}
	return cloneTarget(target), nil
}

func (m *MockClient) Upload(ctx context.Context, auth AuthSession, target UploadTarget, body io.Reader) error {
	m.mu.Lock()
	if _, err := m.authenticate(ctx, auth); err != nil {
		m.mu.Unlock()
		return err
	}
	u := m.uploads[target.TaskID]
	if u == nil || u.owner != auth.Identity.SubjectID || target.URL != u.target.URL {
		m.mu.Unlock()
		return ErrNotFound
	}
	limit := u.metadata.Bytes
	m.mu.Unlock()
	if m.scenario == "upload_error" {
		return ErrRetryable
	}
	var buf bytes.Buffer
	n, err := copyUpload(ctx, &buf, body, limit+1)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil && err != io.EOF {
		return err
	}
	if n != limit {
		return ErrIntegrity
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u.body, u.uploaded = append([]byte(nil), buf.Bytes()...), true
	return nil
}

func (m *MockClient) CompleteUpload(ctx context.Context, auth AuthSession, taskID string, digest Digest) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.authenticate(ctx, auth); err != nil {
		return Task{}, err
	}
	u := m.uploads[taskID]
	if u == nil || u.owner != auth.Identity.SubjectID {
		return Task{}, ErrNotFound
	}
	sum := sha256.Sum256(u.body)
	if !u.uploaded || digest.SHA256 != u.metadata.Digest || digest.Bytes != u.metadata.Bytes ||
		digest.Sessions != u.metadata.Sessions || digest.SchemaVersion != u.metadata.SchemaVersion ||
		hex.EncodeToString(sum[:]) != digest.SHA256 {
		return Task{}, ErrIntegrity
	}
	if u.task.ID != "" {
		return cloneTask(u.task), nil
	}
	now := m.now()
	u.task = Task{ID: taskID, KuAIID: auth.Identity.KuAIID, Status: StatusComplete, CreatedAt: now, UpdatedAt: now,
		Analysis: &Analysis{Headline: "你的 AI 协作报告", Tags: []string{"专注", "可靠"}, Encouragement: "继续保持。"}}
	if m.scenario == "analysis_error" {
		u.task.Status, u.task.Analysis, u.task.ErrorCode = StatusFailed, nil, "analysis_error"
	}
	if m.scenario == "slow" {
		u.task.Status, u.task.Analysis = StatusAnalyzing, nil
		time.AfterFunc(m.delay, func() { m.complete(taskID) })
	}
	return cloneTask(u.task), nil
}

func (m *MockClient) GetTask(ctx context.Context, auth AuthSession, taskID string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.authenticate(ctx, auth); err != nil {
		return Task{}, err
	}
	u := m.uploads[taskID]
	if u == nil || u.owner != auth.Identity.SubjectID || u.task.ID == "" {
		return Task{}, ErrNotFound
	}
	return cloneTask(u.task), nil
}

func (m *MockClient) DownloadPoster(ctx context.Context, auth AuthSession, taskID string) (Poster, error) {
	task, err := m.GetTask(ctx, auth, taskID)
	if err != nil {
		return Poster{}, err
	}
	if task.Status != StatusComplete {
		return Poster{}, ErrInvalid
	}
	if m.scenario == "ticket_error" {
		return Poster{}, ErrRemote
	}
	data := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><text>` + task.KuAIID + `</text></svg>`)
	return Poster{Body: io.NopCloser(bytes.NewReader(data)), ContentType: "image/svg+xml", ContentLength: int64(len(data))}, nil
}

func (m *MockClient) authenticate(ctx context.Context, auth AuthSession) (AuthSession, error) {
	if err := ctx.Err(); err != nil {
		return AuthSession{}, err
	}
	stored, ok := m.sessions[auth.Bearer]
	if !ok || stored.Identity != auth.Identity || !m.now().Before(stored.ExpiresAt) || stored.ExpiresAt != auth.ExpiresAt {
		return AuthSession{}, ErrUnauthenticated
	}
	return stored, nil
}

func (m *MockClient) complete(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.uploads[taskID]
	if u == nil || u.task.Status != StatusAnalyzing {
		return
	}
	u.task.Status, u.task.UpdatedAt = StatusComplete, m.now()
	u.task.Analysis = &Analysis{Headline: "你的 AI 协作报告", Tags: []string{"专注", "可靠"}, Encouragement: "继续保持。"}
}

func validMetadata(v UploadMetadata) bool {
	if v.IdempotencyKey == "" || v.Bytes < 0 || v.Sessions < 0 || v.SchemaVersion == "" || len(v.Digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(v.Digest)
	return err == nil
}
func randomToken(source io.Reader) (string, error) {
	var b [24]byte
	if _, err := io.ReadFull(source, b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

type contextReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

func copyUpload(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if reader, ok := src.(contextReader); ok {
		buffer := make([]byte, 32*1024)
		var total int64
		for total < limit {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			size := int64(len(buffer))
			if remaining := limit - total; remaining < size {
				size = remaining
			}
			n, err := reader.ReadContext(ctx, buffer[:size])
			if n > 0 {
				written, writeErr := dst.Write(buffer[:n])
				total += int64(written)
				if writeErr != nil {
					return total, writeErr
				}
				if written != n {
					return total, io.ErrShortWrite
				}
			}
			if err != nil {
				return total, err
			}
			if n == 0 {
				return total, io.ErrNoProgress
			}
		}
		return total, nil
	}

	done := make(chan struct{})
	watcherDone := make(chan struct{})
	if closer, ok := src.(io.ReadCloser); ok {
		go func() {
			defer close(watcherDone)
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-done:
			}
		}()
	} else {
		close(watcherDone)
	}
	n, err := io.CopyN(dst, &contextCheckingReader{ctx: ctx, reader: src}, limit)
	close(done)
	<-watcherDone
	if ctxErr := ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
}

type contextCheckingReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextCheckingReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func cloneTarget(v UploadTarget) UploadTarget {
	out := v
	out.Headers = make(map[string]string, len(v.Headers))
	for k, x := range v.Headers {
		out.Headers[k] = x
	}
	return out
}
func cloneTask(v Task) Task {
	out := v
	if v.Analysis != nil {
		a := *v.Analysis
		a.Tags = append([]string(nil), v.Analysis.Tags...)
		out.Analysis = &a
	}
	return out
}
