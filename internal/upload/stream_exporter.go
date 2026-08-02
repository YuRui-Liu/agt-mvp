package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

// SessionOpener is the narrow source registry capability needed by the
// exporter. A source.Registry can satisfy it without coupling upload to
// adapter discovery.
type SessionOpener interface {
	Open(context.Context, source.Session) (io.ReadCloser, error)
}

type StreamExporter struct {
	opener     SessionOpener
	client     Client
	limits     Limits
	now        func() time.Time
	tempDir    string
	newBacking func(string) (*os.File, func() error, error)
}

func NewStreamExporter(opener SessionOpener, client Client, limits Limits) *StreamExporter {
	return &StreamExporter{
		opener:     opener,
		client:     client,
		limits:     withDefaultLimits(limits),
		now:        time.Now,
		newBacking: createArtifactBacking,
	}
}

// Artifact is a completed immutable package bound to the file opened during
// construction. Open never resolves the temporary path again and may be called
// concurrently; each caller gets an independent reader lease over those exact
// bytes. A reader opened before Remove remains valid and must be closed by its
// caller. On platforms that refuse to unlink an open file, Remove returns a
// safe error and retains the artifact for retry.
type Artifact struct {
	mu            sync.Mutex
	file          *os.File
	cleanup       func() error
	readers       int
	removePending bool
	Digest        string
	Bytes         int64
	SessionCount  int
	SchemaVersion int
}

func (a *Artifact) Open() (io.ReadCloser, error) {
	if a == nil {
		return nil, errors.New("upload: artifact unavailable")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.removePending || a.file == nil {
		return nil, errors.New("upload: artifact unavailable")
	}
	a.readers++
	return &artifactReader{
		Reader:   io.NewSectionReader(a.file, 0, a.Bytes),
		artifact: a,
	}, nil
}

type artifactReader struct {
	io.Reader
	artifact *Artifact
	once     sync.Once
	closeErr error
}

func (r *artifactReader) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		r.artifact.mu.Lock()
		defer r.artifact.mu.Unlock()
		if r.artifact.readers > 0 {
			r.artifact.readers--
		}
		if r.artifact.removePending && r.artifact.readers == 0 {
			if err := r.artifact.closeFileLocked(); err != nil {
				r.artifact.removePending = false
				r.closeErr = errors.New("upload: artifact removal failed")
			}
		}
	})
	return r.closeErr
}

func (a *Artifact) Remove() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	a.removePending = true
	if a.readers != 0 {
		return nil
	}
	if err := a.closeFileLocked(); err != nil {
		a.removePending = false
		return errors.New("upload: artifact removal failed")
	}
	return nil
}

func (a *Artifact) closeFileLocked() error {
	if a.file == nil {
		return nil
	}
	cleanup := a.cleanup
	if cleanup == nil {
		cleanup = a.file.Close
	}
	if err := cleanup(); err != nil {
		return err
	}
	a.file = nil
	a.cleanup = nil
	return nil
}

// BuildScope reads and redacts one session at a time. Only a single bounded
// session is resident in memory; completed package bytes live in a private
// temporary file.
func (e *StreamExporter) BuildScope(ctx context.Context, scope source.Scope) (_ *Artifact, returnErr error) {
	if e == nil || e.opener == nil || e.now == nil {
		return nil, errors.New("upload: exporter configuration invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	newBacking := e.newBacking
	if newBacking == nil {
		newBacking = createArtifactBacking
	}
	file, cleanup, err := newBacking(e.tempDir)
	if err != nil {
		return nil, errors.New("upload: temporary package unavailable")
	}
	keep := false
	defer func() {
		if keep {
			return
		}
		// Best-effort destruction happens while the private descriptor is still
		// open. If both destruction and unlink fail, return a cleanup-specific
		// error and never return an Artifact.
		truncateErr := file.Truncate(0)
		syncErr := file.Sync()
		cleanupErr := cleanup()
		if cleanupErr != nil && (truncateErr != nil || syncErr != nil) {
			returnErr = errors.New("upload: failed export cleanup incomplete")
		}
	}()

	sum := sha256.New()
	writer := &packageLimitWriter{writer: io.MultiWriter(file, sum), remaining: e.limits.MaxPackageBytes}
	createdAt := e.now().UTC()
	header, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Client        Client `json:"client"`
		Scope         Scope  `json:"scope"`
	}{
		SchemaVersion: 2,
		Client:        e.client,
		Scope: Scope{
			Type:  string(scope.Type),
			Key:   scope.Key,
			Label: scope.Label,
		},
	})
	if err != nil {
		return nil, errors.New("upload: package metadata invalid")
	}
	// Strip the closing object brace and append fields in the same declared
	// order as CanonicalBytes.
	if _, err := writer.Write(header[:len(header)-1]); err != nil {
		return nil, packageWriteError(err)
	}
	if _, err := io.WriteString(writer, `,"sessions":[`); err != nil {
		return nil, packageWriteError(err)
	}

	var total Stats
	for index, session := range scope.Sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reader, err := e.opener.Open(ctx, session)
		if err != nil || reader == nil {
			if reader != nil {
				_ = reader.Close()
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, errors.New("upload: source session open failed")
		}
		events, stats, readErr := scanEvents(&contextReader{ctx: ctx, reader: reader}, e.limits, session.Product+"\x00"+session.ID)
		closeErr := reader.Close()
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
				return nil, readErr
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, readErr
		}
		if closeErr != nil {
			return nil, errors.New("upload: source session close failed")
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		capabilities := make([]string, len(session.Capabilities))
		for capabilityIndex, capability := range session.Capabilities {
			capabilities[capabilityIndex] = string(capability)
		}
		if capabilities == nil {
			capabilities = []string{}
		}
		sessionValue := Session{
			ID: session.ID,
			Source: Source{
				Product:        session.Product,
				FormatVersion:  session.FormatVersion,
				AdapterVersion: session.AdapterVersion,
				Capabilities:   capabilities,
			},
			Events: events,
		}
		if _, err := CanonicalBytes(Package{SchemaVersion: 2, Sessions: []Session{sessionValue}}); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(sessionValue)
		if err != nil {
			return nil, errors.New("upload: session encoding failed")
		}
		if index > 0 {
			if _, err := writer.Write([]byte{','}); err != nil {
				return nil, packageWriteError(err)
			}
		}
		if _, err := writer.Write(encoded); err != nil {
			return nil, packageWriteError(err)
		}
		addStats(&total, stats)
	}
	redaction := struct {
		Replacements  int `json:"replacements"`
		OmittedReads  int `json:"omitted_reads"`
		RemovedFields int `json:"removed_fields"`
	}{total.Replacements, total.OmittedReads, total.RemovedFields}
	redactionJSON, _ := json.Marshal(redaction)
	timeJSON, _ := json.Marshal(createdAt)
	if _, err := io.WriteString(writer, `],"redaction":`); err != nil {
		return nil, packageWriteError(err)
	}
	if _, err := writer.Write(redactionJSON); err != nil {
		return nil, packageWriteError(err)
	}
	if _, err := io.WriteString(writer, `,"created_at":`); err != nil {
		return nil, packageWriteError(err)
	}
	if _, err := writer.Write(timeJSON); err != nil {
		return nil, packageWriteError(err)
	}
	if _, err := writer.Write([]byte{'}'}); err != nil {
		return nil, packageWriteError(err)
	}
	if err := file.Sync(); err != nil {
		return nil, errors.New("upload: package sync failed")
	}
	keep = true
	return &Artifact{
		file:          file,
		cleanup:       cleanup,
		Digest:        digestHex(sum),
		Bytes:         writer.written,
		SessionCount:  len(scope.Sessions),
		SchemaVersion: 2,
	}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

var errPackageLimit = errors.New("package limit")

type packageLimitWriter struct {
	writer    io.Writer
	remaining int64
	written   int64
}

func (w *packageLimitWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, errPackageLimit
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	w.written += int64(n)
	return n, err
}

func packageWriteError(err error) error {
	if errors.Is(err, errPackageLimit) {
		return errors.New("export package exceeds limit")
	}
	return errors.New("upload: package write failed")
}

func digestHex(sum hash.Hash) string {
	return hex.EncodeToString(sum.Sum(nil))
}

// Keep source session order authoritative. GroupScopes already supplies stable
// order; this helper is available to callers assembling scopes manually.
func SortScopeSessions(scope *source.Scope) {
	if scope == nil {
		return
	}
	sort.SliceStable(scope.Sessions, func(i, j int) bool {
		if scope.Sessions[i].Product != scope.Sessions[j].Product {
			return scope.Sessions[i].Product < scope.Sessions[j].Product
		}
		return scope.Sessions[i].ID < scope.Sessions[j].ID
	})
}
