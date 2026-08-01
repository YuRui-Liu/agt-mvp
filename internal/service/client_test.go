package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMockClientHappyPathAndIsolation(t *testing.T) {
	ctx := context.Background()
	client := NewMockClient(MockOptions{Now: time.Now, AnalysisDelay: time.Millisecond})
	if err := client.RequestOTP(ctx, "+8613800138000"); err != nil {
		t.Fatal(err)
	}
	auth, err := client.VerifyOTP(ctx, "+8613800138000", MockOTP)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SubmitConsent(ctx, auth, Consent{Version: ConsentVersion}); err != nil {
		t.Fatal(err)
	}
	meta := UploadMetadata{IdempotencyKey: "one", Bytes: 5, Sessions: 1, SchemaVersion: "v2", Digest: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
	target, err := client.CreateUpload(ctx, auth, meta)
	if err != nil {
		t.Fatal(err)
	}
	again, err := client.CreateUpload(ctx, auth, meta)
	if err != nil || again.TaskID != target.TaskID {
		t.Fatalf("idempotent create = %#v, %v", again, err)
	}
	changed := meta
	changed.Bytes++
	if _, err := client.CreateUpload(ctx, auth, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed create error = %v", err)
	}
	if err := client.Upload(ctx, auth, target, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	task, err := client.CompleteUpload(ctx, auth, target.TaskID, Digest{SHA256: meta.Digest, Bytes: 5, Sessions: 1, SchemaVersion: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	task, err = client.GetTask(ctx, auth, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusComplete {
		t.Fatalf("status = %q", task.Status)
	}
	poster, err := client.DownloadPoster(ctx, auth, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer poster.Body.Close()
	if poster.ContentType != "image/svg+xml" || poster.ContentLength <= 0 {
		t.Fatalf("poster metadata = %#v", poster)
	}
	if _, err := io.ReadAll(poster.Body); err != nil {
		t.Fatal(err)
	}

	other := auth
	other.Bearer = "other"
	if _, err := client.GetTask(ctx, other, task.ID); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("other session error = %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("random unavailable") }

func TestMockClientDoesNotIssueSessionOrUploadIDWhenRandomFails(t *testing.T) {
	ctx := context.Background()
	client := NewMockClient(MockOptions{Rand: failingReader{}})
	if err := client.RequestOTP(ctx, "+8613800138000"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.VerifyOTP(ctx, "+8613800138000", MockOTP); err == nil {
		t.Fatal("VerifyOTP error = nil")
	}

	client = NewMockClient(MockOptions{})
	_ = client.RequestOTP(ctx, "+8613800138000")
	auth, _ := client.VerifyOTP(ctx, "+8613800138000", MockOTP)
	_ = client.SubmitConsent(ctx, auth, Consent{Version: ConsentVersion})
	client.rand = failingReader{}
	if _, err := client.CreateUpload(ctx, auth, UploadMetadata{
		IdempotencyKey: "key", Digest: strings.Repeat("a", 64), Bytes: 1, Sessions: 1, SchemaVersion: "v2",
	}); err == nil {
		t.Fatal("CreateUpload error = nil")
	}
}

type cancelReader struct {
	once    sync.Once
	started chan struct{}
	closed  chan struct{}
}

func newCancelReader() *cancelReader {
	return &cancelReader{started: make(chan struct{}), closed: make(chan struct{})}
}

func (r *cancelReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.closed
	return 0, errors.New("closed")
}
func (r *cancelReader) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}

func TestMockUploadCancellationUnblocksClosableReaderAndStoresNothing(t *testing.T) {
	ctx := context.Background()
	client := NewMockClient(MockOptions{})
	_ = client.RequestOTP(ctx, "+8613800138000")
	auth, _ := client.VerifyOTP(ctx, "+8613800138000", MockOTP)
	_ = client.SubmitConsent(ctx, auth, Consent{Version: ConsentVersion})
	meta := UploadMetadata{IdempotencyKey: "cancel", Digest: strings.Repeat("a", 64), Bytes: 5, Sessions: 1, SchemaVersion: "v2"}
	target, _ := client.CreateUpload(ctx, auth, meta)
	reader := newCancelReader()
	uploadCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- client.Upload(uploadCtx, auth, target, reader) }()
	<-reader.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Upload error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Upload did not return after cancellation")
	}
	if _, err := client.CompleteUpload(ctx, auth, target.TaskID, Digest{
		SHA256: meta.Digest, Bytes: meta.Bytes, Sessions: meta.Sessions, SchemaVersion: meta.SchemaVersion,
	}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("CompleteUpload after canceled upload = %v", err)
	}
}

func TestMockClientIsolatesTwoAuthenticatedSubjects(t *testing.T) {
	ctx := context.Background()
	client := NewMockClient(MockOptions{})
	makeAuth := func(phone string) AuthSession {
		_ = client.RequestOTP(ctx, phone)
		auth, err := client.VerifyOTP(ctx, phone, MockOTP)
		if err != nil {
			t.Fatal(err)
		}
		_ = client.SubmitConsent(ctx, auth, Consent{Version: ConsentVersion})
		return auth
	}
	first, second := makeAuth("+8613800138000"), makeAuth("+8613900139000")
	meta := UploadMetadata{IdempotencyKey: "same", Digest: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", Bytes: 5, Sessions: 1, SchemaVersion: "v2"}
	target, _ := client.CreateUpload(ctx, first, meta)
	if err := client.Upload(ctx, second, target, strings.NewReader("hello")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross upload = %v", err)
	}
	_ = client.Upload(ctx, first, target, strings.NewReader("hello"))
	task, _ := client.CompleteUpload(ctx, first, target.TaskID, Digest{SHA256: meta.Digest, Bytes: 5, Sessions: 1, SchemaVersion: "v2"})
	if _, err := client.GetTask(ctx, second, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross task = %v", err)
	}
	if _, err := client.DownloadPoster(ctx, second, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross poster = %v", err)
	}
}

func TestMockScenarios(t *testing.T) {
	for _, scenario := range []string{"otp_error", "upload_error", "analysis_error", "slow", "ticket_error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			client := NewMockClient(MockOptions{Scenario: scenario, AnalysisDelay: time.Millisecond})
			_ = client.RequestOTP(ctx, "+8613800138000")
			auth, err := client.VerifyOTP(ctx, "+8613800138000", MockOTP)
			if scenario == "otp_error" {
				if !errors.Is(err, ErrUnauthenticated) {
					t.Fatalf("VerifyOTP = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = client.SubmitConsent(ctx, auth, Consent{Version: ConsentVersion})
			meta := UploadMetadata{IdempotencyKey: scenario, Digest: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", Bytes: 5, Sessions: 1, SchemaVersion: "v2"}
			target, _ := client.CreateUpload(ctx, auth, meta)
			err = client.Upload(ctx, auth, target, strings.NewReader("hello"))
			if scenario == "upload_error" {
				if !errors.Is(err, ErrRetryable) {
					t.Fatalf("Upload = %v", err)
				}
				return
			}
			task, err := client.CompleteUpload(ctx, auth, target.TaskID, Digest{SHA256: meta.Digest, Bytes: 5, Sessions: 1, SchemaVersion: "v2"})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "analysis_error":
				if task.Status != StatusFailed {
					t.Fatalf("status = %s", task.Status)
				}
			case "slow":
				if task.Status != StatusAnalyzing {
					t.Fatalf("status = %s", task.Status)
				}
			case "ticket_error":
				if _, err := client.DownloadPoster(ctx, auth, task.ID); !errors.Is(err, ErrRemote) {
					t.Fatalf("poster = %v", err)
				}
			}
		})
	}
}

func TestMockOTPChallengeIsPrivateExpiringBoundedAndOneTime(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	client := NewMockClient(MockOptions{Now: func() time.Time { return now }})
	phone := "+8613800138000"
	if err := client.RequestOTP(context.Background(), phone); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", client.challenges), phone) {
		t.Fatal("challenge state reflects raw phone")
	}
	now = now.Add(5 * time.Minute)
	if _, err := client.VerifyOTP(context.Background(), phone, MockOTP); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired VerifyOTP = %v", err)
	}
	if len(client.challenges) != 0 {
		t.Fatalf("expired challenges = %d", len(client.challenges))
	}

	now = now.Add(time.Second)
	_ = client.RequestOTP(context.Background(), phone)
	if _, err := client.VerifyOTP(context.Background(), phone, "wrong"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong OTP = %v", err)
	}
	if _, err := client.VerifyOTP(context.Background(), phone, MockOTP); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("reused challenge = %v", err)
	}

	for i := 0; i < mockChallengeCapacity; i++ {
		if err := client.RequestOTP(context.Background(), fmt.Sprintf("+86139%08d", i)); err != nil {
			t.Fatalf("RequestOTP(%d) = %v", i, err)
		}
	}
	if err := client.RequestOTP(context.Background(), "+8613700000000"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("over-capacity RequestOTP = %v", err)
	}
}

func TestMockOTPChallengeCanOnlyBeConsumedOnceConcurrently(t *testing.T) {
	client := NewMockClient(MockOptions{})
	phone := "+8613800138000"
	_ = client.RequestOTP(context.Background(), phone)
	var successes int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.VerifyOTP(context.Background(), phone, MockOTP); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successful verifications = %d, want 1", successes)
	}
}

func TestMockClientRequiresExactConsentAndCompletion(t *testing.T) {
	ctx := context.Background()
	client := NewMockClient(MockOptions{})
	_ = client.RequestOTP(ctx, "+8613800138000")
	auth, _ := client.VerifyOTP(ctx, "+8613800138000", MockOTP)
	meta := UploadMetadata{IdempotencyKey: "one", Bytes: 1, Sessions: 1, SchemaVersion: "v2", Digest: strings.Repeat("a", 64)}
	if _, err := client.CreateUpload(ctx, auth, meta); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("create before consent = %v", err)
	}
	if err := client.SubmitConsent(ctx, auth, Consent{Version: "old"}); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("old consent = %v", err)
	}
}
