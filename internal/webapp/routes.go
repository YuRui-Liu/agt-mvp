package webapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/service"
	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/catalog"
	"github.com/YuRui-Liu/agt-mvp/internal/upload"
)

type App struct {
	LaunchToken    string
	Registry       *source.Registry
	Catalog        []catalog.Definition
	ScopeSecret    []byte
	Exporter       *upload.StreamExporter
	Service        service.Client
	ServiceMode    string
	ServiceHost    string
	Clock          func() time.Time
	RemoveArtifact func(*upload.Artifact) error
	runtimeMu      sync.Mutex
	runtime        *appHandler
}

type preparedArtifact struct {
	artifact        *upload.Artifact
	expires         time.Time
	claimed         bool
	taskID          string
	idempotencyKey  string
	target          service.UploadTarget
	uploaded        bool
	subjectID       string
	pendingCleanup  bool
	artifactRemoved bool
	receipt         *submissionReceipt
}

type appHandler struct {
	app      *App
	mu       sync.Mutex
	prepared map[string]*preparedArtifact
	auth     map[string]service.AuthSession
	stop     chan struct{}
	stopOnce sync.Once
	closing  bool
}

type scopeView struct {
	Key          string           `json:"key"`
	Type         source.ScopeType `json:"type"`
	Label        string           `json:"label"`
	Agents       []string         `json:"agents"`
	SessionCount int              `json:"session_count"`
	Capabilities []string         `json:"capabilities"`
	StartedAt    time.Time        `json:"started_at,omitempty"`
	EndedAt      time.Time        `json:"ended_at,omitempty"`
	Bytes        int64            `json:"bytes,omitempty"`
	Status       string           `json:"status"`
	Selectable   bool             `json:"selectable"`
}

type sourceView struct {
	Product     string         `json:"product"`
	DisplayName string         `json:"display_name"`
	Status      catalog.Status `json:"status"`
	Supported   bool           `json:"supported"`
	Enabled     bool           `json:"enabled"`
	Detected    bool           `json:"detected"`
}

type submissionReceipt struct {
	ReceiptID   string    `json:"receipt_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	Status      string    `json:"status"`
}

func Handler(app *App) http.Handler {
	h := &appHandler{app: app, prepared: map[string]*preparedArtifact{}, auth: map[string]service.AuthSession{},
		stop: make(chan struct{})}
	if app != nil {
		app.runtimeMu.Lock()
		app.runtime = h
		app.runtimeMu.Unlock()
	}
	go h.cleanupLoop()
	return h
}

func (app *App) now() time.Time {
	if app != nil && app.Clock != nil {
		return app.Clock()
	}
	return time.Now()
}

func (app *App) CleanupExpired() {
	if app == nil {
		return
	}
	app.runtimeMu.Lock()
	h := app.runtime
	app.runtimeMu.Unlock()
	if h != nil {
		h.cleanupExpired(app.now())
	}
}

func (app *App) Close() {
	if app == nil {
		return
	}
	app.runtimeMu.Lock()
	h := app.runtime
	app.runtime = nil
	app.runtimeMu.Unlock()
	if h != nil {
		h.close()
	}
}

func (h *appHandler) cleanupLoop() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if h.app != nil {
				h.cleanupExpired(h.app.now())
			}
		case <-h.stop:
			return
		}
	}
}

func (h *appHandler) close() {
	h.mu.Lock()
	h.closing = true
	h.cleanupPrepared(time.Time{})
	clear(h.auth)
	h.stopIfCleanLocked()
	h.mu.Unlock()
}

func (h *appHandler) stopIfCleanLocked() {
	if h.closing && len(h.prepared) == 0 {
		h.stopOnce.Do(func() { close(h.stop) })
	}
}

func (h *appHandler) removeArtifact(artifact *upload.Artifact) error {
	if h.app != nil && h.app.RemoveArtifact != nil {
		return h.app.RemoveArtifact(artifact)
	}
	return artifact.Remove()
}

func (h *appHandler) cleanupExpired(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupPrepared(now)
	for bearer, auth := range h.auth {
		if !now.Before(auth.ExpiresAt) {
			delete(h.auth, bearer)
		}
	}
}

func validateApp(app *App) error {
	if app == nil || strings.TrimSpace(app.LaunchToken) == "" || app.Registry == nil ||
		len(app.ScopeSecret) < 32 || app.Exporter == nil || app.Service == nil {
		return errors.New("web application configuration invalid")
	}
	return nil
}

func (h *appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.app != nil {
		h.cleanupExpired(h.app.now())
	}
	for key, value := range securityHeaders {
		w.Header().Set(key, value)
	}
	if validateApp(h.app) != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "服务暂时不可用")
		return
	}
	if h.exchangeLaunchToken(w, r) {
		return
	}
	if !h.hasLaunchToken(r) {
		writeError(w, http.StatusForbidden, "launch_token_required", "请从本地启动入口访问")
		return
	}
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		writeIndexAsset(w, h.app.ServiceMode, h.app.ServiceHost)
	case r.URL.Path == "/application" && r.Method == http.MethodGet:
		writeAsset(w, "application.html", "text/html; charset=utf-8")
	case r.URL.Path == "/assets/app.js" && r.Method == http.MethodGet:
		writeAsset(w, "app.js", "application/javascript; charset=utf-8")
	case r.URL.Path == "/assets/flow_logic.js" && r.Method == http.MethodGet:
		writeAsset(w, "flow_logic.js", "application/javascript; charset=utf-8")
	case r.URL.Path == "/assets/styles.css" && r.Method == http.MethodGet:
		writeAsset(w, "styles.css", "text/css; charset=utf-8")
	case r.URL.Path == "/assets/application.js" && r.Method == http.MethodGet:
		writeAsset(w, "application.js", "application/javascript; charset=utf-8")
	case r.URL.Path == "/api/applications" && r.Method == http.MethodPost:
		h.createApplication(w, r)
	case r.URL.Path == "/api/scopes" && r.Method == http.MethodGet:
		h.scopes(w, r)
	case r.URL.Path == "/api/prepare" && r.Method == http.MethodPost:
		h.prepare(w, r)
	case r.URL.Path == "/api/auth/request-code" && r.Method == http.MethodPost:
		h.requestOTP(w, r)
	case r.URL.Path == "/api/auth/verify" && r.Method == http.MethodPost:
		h.verifyOTP(w, r)
	case r.URL.Path == "/api/consent" && r.Method == http.MethodPost:
		h.consent(w, r)
	case r.URL.Path == "/api/tasks" && r.Method == http.MethodPost:
		h.createTask(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "请求的资源不存在")
	}
}

func (h *appHandler) createApplication(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Position string `json:"position"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || !strings.Contains(input.Email, "@") ||
		strings.TrimSpace(input.Position) == "" {
		writeError(w, http.StatusBadRequest, "application_invalid", "请完整填写投递信息")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "accepted"})
}

func (h *appHandler) exchangeLaunchToken(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.Path != "/" || !r.URL.Query().Has("token") {
		return false
	}
	if !secureEqual(r.URL.Query().Get("token"), h.app.LaunchToken) {
		writeError(w, http.StatusForbidden, "launch_token_invalid", "启动凭证无效")
		return true
	}
	http.SetCookie(w, &http.Cookie{Name: launchCookieName, Value: h.app.LaunchToken, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return true
}

func (h *appHandler) hasLaunchToken(r *http.Request) bool {
	if secureEqual(r.Header.Get("X-Kuai-Token"), h.app.LaunchToken) {
		return true
	}
	cookie, err := r.Cookie(launchCookieName)
	return err == nil && secureEqual(cookie.Value, h.app.LaunchToken)
}

func secureEqual(got, want string) bool {
	return got != "" && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (h *appHandler) scanScopes(ctx context.Context) ([]source.Scope, source.ScanResult, error) {
	scan, err := h.app.Registry.Scan(ctx)
	if err != nil {
		return nil, source.ScanResult{}, err
	}
	scopes, err := source.GroupScopes(scan.Sessions, h.app.ScopeSecret)
	return scopes, scan, err
}

func (h *appHandler) scopes(w http.ResponseWriter, r *http.Request) {
	scopes, scan, err := h.scanScopes(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "scan_failed", "本地扫描暂时失败")
		return
	}
	views := make([]scopeView, 0, len(scopes))
	for _, scope := range scopes {
		agents, caps := map[string]struct{}{}, map[string]struct{}{}
		var started, ended time.Time
		var bytes int64
		selectable := len(scope.Sessions) > 0
		for _, session := range scope.Sessions {
			agents[session.Product] = struct{}{}
			if scan.Sources[session.Product].State != source.SourceReady {
				selectable = false
			}
			for _, capability := range session.Capabilities {
				caps[string(capability)] = struct{}{}
			}
			if started.IsZero() || (!session.StartedAt.IsZero() && session.StartedAt.Before(started)) {
				started = session.StartedAt
			}
			if session.EndedAt.After(ended) {
				ended = session.EndedAt
			}
			bytes += session.Usage["bytes"]
		}
		view := scopeView{Key: scope.Key, Type: scope.Type, Label: scope.Label, SessionCount: scope.SessionCount,
			Agents: keys(agents), Capabilities: keys(caps), StartedAt: started, EndedAt: ended, Bytes: bytes,
			Status: "ready", Selectable: selectable}
		if !selectable {
			view.Status = "detected_unsupported"
		}
		views = append(views, view)
	}
	for _, definition := range h.app.Catalog {
		if definition.Supported || !definition.Detected {
			continue
		}
		views = append(views, scopeView{
			Type: source.ScopeSessionCollection, Label: definition.DisplayName,
			Agents: []string{definition.Product}, Capabilities: []string{},
			Status: string(catalog.DetectedUnsupported), Selectable: false,
		})
	}
	sourceViews := make([]sourceView, 0, len(h.app.Catalog))
	for _, definition := range h.app.Catalog {
		sourceViews = append(sourceViews, sourceView{
			Product: definition.Product, DisplayName: definition.DisplayName, Status: definition.Status,
			Supported: definition.Supported, Enabled: definition.Enabled, Detected: definition.Detected,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": views, "sources": sourceViews})
}

func keys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (h *appHandler) prepare(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ScopeKey string `json:"scope_key"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	scopes, scan, err := h.scanScopes(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "scan_failed", "本地扫描暂时失败")
		return
	}
	var selected *source.Scope
	for index := range scopes {
		if scopes[index].Key == input.ScopeKey {
			selected = &scopes[index]
			break
		}
	}
	if selected == nil || len(selected.Sessions) == 0 {
		writeError(w, http.StatusBadRequest, "scope_invalid", "请选择可评估范围")
		return
	}
	for _, session := range selected.Sessions {
		if scan.Sources[session.Product].State != source.SourceReady {
			writeError(w, http.StatusBadRequest, "scope_unavailable", "该范围的来源状态已变化，请重新扫描")
			return
		}
	}
	artifact, err := h.app.Exporter.BuildScope(r.Context(), *selected)
	if err != nil {
		writeError(w, http.StatusBadRequest, "prepare_failed", "本地导出失败，请检查会话后重试")
		return
	}
	id := artifact.Digest[:24]
	now := h.app.now()
	h.mu.Lock()
	h.cleanupPrepared(now)
	if previous := h.prepared[id]; previous != nil && previous.artifact != artifact {
		_ = h.removeArtifact(artifact)
		artifact = previous.artifact
	} else {
		h.prepared[id] = &preparedArtifact{artifact: artifact, expires: now.Add(15 * time.Minute)}
	}
	h.mu.Unlock()
	progress := make([]map[string]any, artifact.SessionCount)
	for index := range progress {
		progress[index] = map[string]any{"index": index + 1, "status": "exported"}
	}
	writeJSON(w, http.StatusOK, map[string]any{"preparation_id": id, "session_count": artifact.SessionCount,
		"bytes": artifact.Bytes, "digest": artifact.Digest, "session_progress": progress})
}

func (h *appHandler) cleanupPrepared(now time.Time) {
	for id, entry := range h.prepared {
		expired := now.IsZero() || !now.Before(entry.expires)
		if !expired && !entry.pendingCleanup {
			continue
		}
		if entry.claimed {
			entry.pendingCleanup = true
			continue
		}
		if !entry.artifactRemoved {
			if h.removeArtifact(entry.artifact) != nil {
				entry.pendingCleanup = true
				continue
			}
			entry.artifactRemoved = true
			entry.pendingCleanup = false
		}
		if expired {
			delete(h.prepared, id)
		}
	}
	h.stopIfCleanLocked()
}

func (h *appHandler) finishPendingCleanupLocked(id string, entry *preparedArtifact) {
	if entry == nil || entry.claimed || !entry.pendingCleanup {
		return
	}
	if h.removeArtifact(entry.artifact) == nil {
		entry.artifactRemoved = true
		entry.pendingCleanup = false
		if h.closing || !h.app.now().Before(entry.expires) {
			delete(h.prepared, id)
		}
	}
	h.stopIfCleanLocked()
}

func (h *appHandler) requestOTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Phone string `json:"phone"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.app.Service.RequestOTP(r.Context(), input.Phone); err != nil {
		writeServiceError(w, err, "code_request_failed", "无法发送验证码，请检查手机号")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *appHandler) verifyOTP(w http.ResponseWriter, r *http.Request) {
	var input struct{ Phone, Code string }
	if !decodeJSON(w, r, &input) {
		return
	}
	auth, err := h.app.Service.VerifyOTP(r.Context(), input.Phone, input.Code)
	if err != nil {
		writeServiceError(w, err, "verification_failed", "验证码无效或已过期")
		return
	}
	h.mu.Lock()
	if len(h.auth) >= 1024 {
		h.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "auth_capacity", "认证会话暂时已满，请稍后重试")
		return
	}
	h.auth[auth.Bearer] = auth
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, auth)
}

func (h *appHandler) authenticate(w http.ResponseWriter, r *http.Request) (service.AuthSession, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "authentication_required", "请先完成身份验证")
		return service.AuthSession{}, false
	}
	bearer := strings.TrimPrefix(header, "Bearer ")
	if bearer == "" || strings.ContainsAny(bearer, " \t\r\n") {
		writeError(w, http.StatusUnauthorized, "authentication_required", "请先完成身份验证")
		return service.AuthSession{}, false
	}
	h.mu.Lock()
	auth, ok := h.auth[bearer]
	if ok && !h.app.now().Before(auth.ExpiresAt) {
		delete(h.auth, bearer)
		ok = false
	}
	h.mu.Unlock()
	if !ok || auth.Bearer != bearer {
		writeError(w, http.StatusUnauthorized, "authentication_invalid", "身份验证已失效")
		return service.AuthSession{}, false
	}
	return auth, true
}

func (h *appHandler) consent(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var input service.Consent
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.app.Service.SubmitConsent(r.Context(), auth, input); err != nil {
		writeServiceError(w, err, "consent_required", "请明确确认完整用途授权")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *appHandler) createTask(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		PreparationID  string `json:"preparation_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.PreparationID == "" || input.IdempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "task_invalid", "准备结果或重试标识无效")
		return
	}
	h.mu.Lock()
	entry := h.prepared[input.PreparationID]
	if entry == nil || entry.claimed {
		h.mu.Unlock()
		writeError(w, http.StatusBadRequest, "preparation_invalid", "准备结果已失效")
		return
	}
	if entry.taskID != "" {
		if entry.subjectID != auth.Identity.SubjectID {
			h.mu.Unlock()
			writeError(w, http.StatusConflict, "preparation_owner_conflict", "该准备结果属于其他认证主体")
			return
		}
		if entry.idempotencyKey != input.IdempotencyKey {
			h.mu.Unlock()
			writeError(w, http.StatusConflict, "upload_retry_conflict", "请使用当前评估的安全重试标识")
			return
		}
		receipt := *entry.receipt
		h.mu.Unlock()
		writeJSON(w, http.StatusOK, receipt)
		return
	}
	if entry.subjectID != "" && entry.subjectID != auth.Identity.SubjectID {
		h.mu.Unlock()
		writeError(w, http.StatusConflict, "preparation_owner_conflict", "该准备结果属于其他认证主体")
		return
	}
	if entry.subjectID == "" {
		entry.subjectID = auth.Identity.SubjectID
	}
	if entry.idempotencyKey != "" && entry.idempotencyKey != input.IdempotencyKey {
		h.mu.Unlock()
		writeError(w, http.StatusConflict, "upload_retry_conflict", "请使用当前评估的安全重试标识")
		return
	}
	if entry.idempotencyKey == "" {
		entry.idempotencyKey = input.IdempotencyKey
	}
	entry.claimed = true
	target := entry.target
	uploaded := entry.uploaded
	h.mu.Unlock()
	success := false
	defer func() {
		h.mu.Lock()
		if !success && entry.taskID == "" {
			entry.claimed = false
		}
		h.finishPendingCleanupLocked(input.PreparationID, entry)
		h.mu.Unlock()
	}()
	artifact := entry.artifact
	metadata := service.UploadMetadata{IdempotencyKey: input.IdempotencyKey, Digest: artifact.Digest,
		Bytes: artifact.Bytes, Sessions: artifact.SessionCount, SchemaVersion: fmt.Sprint(artifact.SchemaVersion)}
	if !h.preparationActive(entry) {
		writeError(w, http.StatusBadRequest, "preparation_expired", "准备结果已过期，请重新准备")
		return
	}
	if target.TaskID == "" {
		var err error
		target, err = h.app.Service.CreateUpload(r.Context(), auth, metadata)
		if err != nil {
			writeServiceError(w, err, "upload_create_failed", "无法创建上传任务")
			return
		}
		h.mu.Lock()
		entry.target = target
		h.mu.Unlock()
	}
	if !h.preparationActive(entry) {
		writeError(w, http.StatusBadRequest, "preparation_expired", "准备结果已过期，请重新准备")
		return
	}
	if !uploaded {
		reader, err := artifact.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, "preparation_invalid", "准备结果已失效")
			return
		}
		err = h.app.Service.Upload(r.Context(), auth, target, reader)
		closeErr := reader.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			writeServiceError(w, err, "upload_retryable", "上传暂时中断，可安全重试")
			return
		}
		h.mu.Lock()
		entry.uploaded = true
		h.mu.Unlock()
	}
	if !h.preparationActive(entry) {
		writeError(w, http.StatusBadRequest, "preparation_expired", "准备结果已过期，请重新准备")
		return
	}
	task, err := h.app.Service.CompleteUpload(r.Context(), auth, target.TaskID, service.Digest{
		SHA256: artifact.Digest, Bytes: artifact.Bytes, Sessions: artifact.SessionCount, SchemaVersion: fmt.Sprint(artifact.SchemaVersion)})
	if err != nil {
		writeServiceError(w, err, "upload_complete_failed", "上传确认失败，可安全重试")
		return
	}
	receipt := h.receipt(task)
	h.mu.Lock()
	entry.taskID, entry.claimed = task.ID, false
	entry.receipt = &receipt
	if h.removeArtifact(artifact) == nil {
		entry.artifactRemoved = true
	} else {
		entry.pendingCleanup = true
	}
	h.mu.Unlock()
	success = true
	writeJSON(w, http.StatusOK, receipt)
}

func (h *appHandler) receipt(task service.Task) submissionReceipt {
	mac := hmac.New(sha256.New, h.app.ScopeSecret)
	_, _ = mac.Write([]byte("kuai-submission-receipt:v1\x00"))
	_, _ = mac.Write([]byte(task.ID))
	submittedAt := task.CreatedAt
	if submittedAt.IsZero() {
		submittedAt = h.app.now()
	}
	return submissionReceipt{
		ReceiptID:   fmt.Sprintf("KW-%X", mac.Sum(nil)[:16]),
		SubmittedAt: submittedAt,
		Status:      "submitted",
	}
}

func (h *appHandler) preparationActive(entry *preparedArtifact) bool {
	if entry != nil && h.app.now().Before(entry.expires) {
		return true
	}
	if entry != nil && entry.artifact != nil {
		_ = h.removeArtifact(entry.artifact)
	}
	return false
}

func writeServiceError(w http.ResponseWriter, err error, code, message string) {
	status := http.StatusBadRequest
	if errors.Is(err, service.ErrUnauthenticated) {
		status = http.StatusUnauthorized
	} else if errors.Is(err, service.ErrRetryable) || errors.Is(err, service.ErrRemote) || errors.Is(err, service.ErrCapacity) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, service.ErrNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, service.ErrConflict) {
		status = http.StatusConflict
	}
	writeError(w, status, code, message)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "content_type_required", "请求必须使用 JSON")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求内容无效")
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求内容无效")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
