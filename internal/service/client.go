package service

import (
	"context"
	"errors"
	"io"
	"time"
)

const ConsentVersion = "kuai-consent-v1"

var (
	ErrInvalid          = errors.New("invalid service request")
	ErrUnauthenticated  = errors.New("unauthenticated")
	ErrConsentRequired  = errors.New("exact consent version required")
	ErrConflict         = errors.New("idempotency conflict")
	ErrNotFound         = errors.New("not found")
	ErrIntegrity        = errors.New("upload integrity check failed")
	ErrRetryable        = errors.New("retryable service failure")
	ErrRemote           = errors.New("remote service failure")
	ErrResponseTooLarge = errors.New("service response too large")
	ErrUnsupportedMedia = errors.New("unsupported media type")
	ErrCapacity         = errors.New("service state capacity exceeded")
)

type Identity struct {
	SubjectID string `json:"subject_id"`
	KuAIID    string `json:"kuai_id"`
}

type AuthSession struct {
	Identity  Identity  `json:"identity"`
	Bearer    string    `json:"bearer"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Consent struct {
	Version string `json:"version"`
}

type UploadMetadata struct {
	IdempotencyKey string `json:"idempotency_key"`
	Digest         string `json:"digest"`
	Bytes          int64  `json:"bytes"`
	Sessions       int    `json:"sessions"`
	SchemaVersion  string `json:"schema_version"`
}

type UploadTarget struct {
	TaskID  string            `json:"task_id"`
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type Digest struct {
	SHA256        string `json:"digest"`
	Bytes         int64  `json:"bytes"`
	Sessions      int    `json:"sessions"`
	SchemaVersion string `json:"schema_version"`
}

type Status string

const (
	StatusQueued    Status = "queued"
	StatusAnalyzing Status = "analyzing"
	StatusComplete  Status = "complete"
	StatusFailed    Status = "failed"
)

type Analysis struct {
	Headline      string   `json:"headline"`
	Tags          []string `json:"tags"`
	Encouragement string   `json:"encouragement"`
}

type Task struct {
	ID        string    `json:"id"`
	KuAIID    string    `json:"kuai_id,omitempty"`
	Status    Status    `json:"status"`
	Analysis  *Analysis `json:"analysis,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Poster struct {
	Body          io.ReadCloser
	ContentType   string
	ContentLength int64
}

type Client interface {
	RequestOTP(context.Context, string) error
	VerifyOTP(context.Context, string, string) (AuthSession, error)
	SubmitConsent(context.Context, AuthSession, Consent) error
	CreateUpload(context.Context, AuthSession, UploadMetadata) (UploadTarget, error)
	Upload(context.Context, AuthSession, UploadTarget, io.Reader) error
	CompleteUpload(context.Context, AuthSession, string, Digest) (Task, error)
	GetTask(context.Context, AuthSession, string) (Task, error)
	DownloadPoster(context.Context, AuthSession, string) (Poster, error)
}

type ServiceClient = Client

type Error struct {
	Kind       error
	StatusCode int
	Code       string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return e.Kind.Error() + ": " + e.Code
	}
	return e.Kind.Error()
}

func (e *Error) Unwrap() error { return e.Kind }
