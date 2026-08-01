package mocksvc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/upload"
)

const ConsentVersion = "kuai-consent-v1"

const packageRetention = 30 * 24 * time.Hour

var (
	ErrConsentRequired     = errors.New("exact consent version required")
	ErrUploadRetryable     = errors.New("upload failed; retry is safe")
	ErrInvalidTask         = errors.New("invalid task request")
	ErrTaskNotFound        = errors.New("task not found")
	ErrUnknownScenario     = errors.New("unknown mock scenario")
	ErrIdentityMismatch    = errors.New("identity does not match trusted store")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different request")
	ErrTaskServiceClosed   = errors.New("task service closed")
)

type TaskStatus string

const (
	TaskQueued    TaskStatus = "queued"
	TaskAnalyzing TaskStatus = "analyzing"
	TaskComplete  TaskStatus = "complete"
	TaskFailed    TaskStatus = "failed"
)

type Analysis struct {
	Headline      string   `json:"headline"`
	Tags          []string `json:"tags"`
	Encouragement string   `json:"encouragement"`
}

type Task struct {
	ID        string     `json:"id"`
	SubjectID string     `json:"-"`
	KuAIID    string     `json:"kuai_id"`
	Status    TaskStatus `json:"status"`
	Analysis  *Analysis  `json:"analysis,omitempty"`
	ErrorCode string     `json:"error_code,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type CreateTaskRequest struct {
	Identity       Identity
	Package        upload.Package
	ConsentVersion string
	IdempotencyKey string
}

type ConsentRecord struct {
	SubjectID string
	Version   string
	CreatedAt time.Time
}

type storedPackage struct {
	value     upload.Package
	createdAt time.Time
}

type TaskService struct {
	identityStore IdentityStore
	clock         Clock
	scenario      string
	analysisDelay time.Duration

	mu        sync.RWMutex
	tasks     map[string]Task
	byRequest map[string]string
	digests   map[string][sha256.Size]byte
	latest    map[string]string
	packages  map[string]storedPackage
	consents  map[string]ConsentRecord
	timers    map[string]*time.Timer
	closed    bool
}

func NewTaskService(store IdentityStore, clock Clock, scenario string, analysisDelay time.Duration) *TaskService {
	if clock == nil {
		clock = time.Now
	}
	if analysisDelay < 0 {
		analysisDelay = 0
	}
	return &TaskService{
		identityStore: store,
		clock:         clock,
		scenario:      scenario,
		analysisDelay: analysisDelay,
		tasks:         make(map[string]Task),
		byRequest:     make(map[string]string),
		digests:       make(map[string][sha256.Size]byte),
		latest:        make(map[string]string),
		packages:      make(map[string]storedPackage),
		consents:      make(map[string]ConsentRecord),
		timers:        make(map[string]*time.Timer),
	}
}

func (s *TaskService) CreateTask(request CreateTaskRequest) (Task, error) {
	if s == nil || s.clock == nil || s.identityStore == nil || request.Identity.SubjectID == "" ||
		request.Identity.KuAIID == "" || request.IdempotencyKey == "" {
		return Task{}, ErrInvalidTask
	}
	if request.ConsentVersion != ConsentVersion {
		return Task{}, ErrConsentRequired
	}
	s.cleanupExpiredPackages(s.clock())
	switch s.scenario {
	case "success", "slow", "upload_error", "analysis_error", "ticket_error", "otp_error":
	default:
		return Task{}, ErrUnknownScenario
	}

	trusted, exists, err := s.identityStore.Get(request.Identity.SubjectID)
	if err != nil {
		return Task{}, err
	}
	identityMatches := exists &&
		trusted.SubjectID == request.Identity.SubjectID &&
		trusted.KuAIID == request.Identity.KuAIID
	clonedPackage, packageJSON, err := clonePackage(request.Package)
	if err != nil {
		return Task{}, ErrInvalidTask
	}
	if s.scenario == "upload_error" {
		if !identityMatches {
			return Task{}, ErrIdentityMismatch
		}
		return Task{}, ErrUploadRetryable
	}
	digest := taskRequestDigest(request, packageJSON)
	requestKey := request.Identity.SubjectID + "\x00" + request.IdempotencyKey
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Task{}, ErrTaskServiceClosed
	}
	if taskID, exists := s.byRequest[requestKey]; exists {
		if s.digests[requestKey] != digest {
			s.mu.Unlock()
			if !identityMatches {
				return Task{}, errors.Join(ErrIdempotencyConflict, ErrIdentityMismatch)
			}
			return Task{}, ErrIdempotencyConflict
		}
		if !identityMatches {
			s.mu.Unlock()
			return Task{}, ErrIdentityMismatch
		}
		task := cloneTask(s.tasks[taskID])
		s.mu.Unlock()
		return task, nil
	}
	if !identityMatches {
		s.mu.Unlock()
		return Task{}, ErrIdentityMismatch
	}
	taskID, err := randomID()
	if err != nil {
		s.mu.Unlock()
		return Task{}, err
	}
	now := s.clock()
	task := Task{
		ID:        taskID,
		SubjectID: trusted.SubjectID,
		KuAIID:    trusted.KuAIID,
		Status:    TaskQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.byRequest[requestKey] = task.ID
	s.digests[requestKey] = digest
	s.latest[task.SubjectID] = task.ID
	s.packages[task.ID] = storedPackage{value: clonedPackage, createdAt: now}
	s.consents[task.ID] = ConsentRecord{
		SubjectID: task.SubjectID,
		Version:   request.ConsentVersion,
		CreatedAt: now,
	}
	s.tasks[task.ID] = task

	switch s.scenario {
	case "success", "ticket_error", "otp_error":
		task = s.completeTaskLocked(task.ID)
	case "analysis_error":
		task.Status = TaskFailed
		task.ErrorCode = "analysis_error"
		task.UpdatedAt = s.clock()
		s.tasks[task.ID] = task
	case "slow":
		s.timers[task.ID] = time.AfterFunc(s.analysisDelay, func() {
			s.completeSlowTask(task.ID)
		})
	}
	s.mu.Unlock()
	return cloneTask(task), nil
}

// ExistingTask checks the idempotency index without requiring the upload package again.
func (s *TaskService) ExistingTask(subjectID, idempotencyKey string) (Task, bool) {
	if s == nil {
		return Task{}, false
	}
	s.cleanupExpiredPackages(s.clock())
	s.mu.RLock()
	defer s.mu.RUnlock()
	taskID, ok := s.byRequest[subjectID+"\x00"+idempotencyKey]
	if !ok {
		return Task{}, false
	}
	return cloneTask(s.tasks[taskID]), true
}

// Task returns a task only when it belongs to subjectID.
func (s *TaskService) Task(subjectID, taskID string) (Task, bool) {
	if s == nil {
		return Task{}, false
	}
	s.cleanupExpiredPackages(s.clock())
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[taskID]
	if !ok || task.SubjectID != subjectID {
		return Task{}, false
	}
	return cloneTask(task), true
}

func (s *TaskService) GetTask(subjectID, taskID string) (Task, error) {
	task, ok := s.Task(subjectID, taskID)
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	return task, nil
}

func (s *TaskService) LatestTask(subjectID string) (Task, bool) {
	if s == nil {
		return Task{}, false
	}
	s.cleanupExpiredPackages(s.clock())
	s.mu.RLock()
	defer s.mu.RUnlock()
	taskID, ok := s.latest[subjectID]
	if !ok {
		return Task{}, false
	}
	return cloneTask(s.tasks[taskID]), true
}

// CompletePending deterministically drains slow tasks in tests without sleeping.
func (s *TaskService) CompletePending() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for taskID, task := range s.tasks {
		if task.Status == TaskQueued || task.Status == TaskAnalyzing {
			if timer, ok := s.timers[taskID]; ok {
				timer.Stop()
				delete(s.timers, taskID)
			}
			task.Status = TaskAnalyzing
			task.UpdatedAt = s.clock()
			s.tasks[taskID] = task
			s.completeTaskLocked(taskID)
		}
	}
}

// Close stops pending background work. It is safe to call more than once.
func (s *TaskService) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for taskID, timer := range s.timers {
		timer.Stop()
		delete(s.timers, taskID)
	}
	return nil
}

func (s *TaskService) Cleanup(now time.Time) {
	s.cleanupExpiredPackages(now)
}

func (s *TaskService) cleanupExpiredPackages(now time.Time) {
	if s == nil {
		return
	}
	cutoff := now.Add(-packageRetention)
	s.mu.Lock()
	defer s.mu.Unlock()
	for taskID, pkg := range s.packages {
		if !pkg.createdAt.After(cutoff) {
			delete(s.packages, taskID)
		}
	}
}

func (s *TaskService) HasPackage(taskID string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.packages[taskID]
	return ok
}

func (s *TaskService) completeSlowTask(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.timers, taskID)
	if s.closed {
		return
	}
	s.completeTaskLocked(taskID)
}

func (s *TaskService) completeTaskLocked(taskID string) Task {
	task, ok := s.tasks[taskID]
	if !ok || (task.Status != TaskQueued && task.Status != TaskAnalyzing) {
		return task
	}
	task.Status = TaskAnalyzing
	task.UpdatedAt = s.clock()
	s.tasks[taskID] = task
	task.Status = TaskComplete
	task.Analysis = &Analysis{
		Headline:      "善于拆解复杂目标",
		Tags:          []string{"目标引导", "工程协作", "持续迭代"},
		Encouragement: "继续保持好奇与行动。",
	}
	task.ErrorCode = ""
	task.UpdatedAt = s.clock()
	s.tasks[taskID] = task
	return task
}

func randomID() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func clonePackage(pkg upload.Package) (upload.Package, []byte, error) {
	encoded, err := json.Marshal(pkg)
	if err != nil {
		return upload.Package{}, nil, err
	}
	var cloned upload.Package
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return upload.Package{}, nil, err
	}
	return cloned, encoded, nil
}

func taskRequestDigest(request CreateTaskRequest, packageJSON []byte) [sha256.Size]byte {
	digestInput := struct {
		SubjectID      string          `json:"subject_id"`
		KuAIID         string          `json:"kuai_id"`
		Package        json.RawMessage `json:"package"`
		ConsentVersion string          `json:"consent_version"`
	}{
		SubjectID:      request.Identity.SubjectID,
		KuAIID:         request.Identity.KuAIID,
		Package:        packageJSON,
		ConsentVersion: request.ConsentVersion,
	}
	encoded, _ := json.Marshal(digestInput)
	return sha256.Sum256(encoded)
}

func cloneTask(task Task) Task {
	if task.Analysis == nil {
		return task
	}
	analysis := *task.Analysis
	analysis.Tags = append([]string(nil), task.Analysis.Tags...)
	task.Analysis = &analysis
	return task
}
