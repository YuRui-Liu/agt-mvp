package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	requestOTPPath   = "/v1/auth/otp/request"
	verifyOTPPath    = "/v1/auth/otp/verify"
	consentPath      = "/v1/consents"
	uploadsPath      = "/v1/uploads"
	tasksPath        = "/v1/tasks/"
	maxErrorBytes    = 16 << 10
	maxResponseBytes = 1 << 20
	maxPosterBytes   = 20 << 20
)

var allowedUploadHeaders = map[string]bool{
	"content-type": true, "content-length": true, "content-md5": true,
	"x-amz-checksum-sha256": true, "x-ms-blob-type": true,
}

type HTTPClient struct {
	base   *url.URL
	client *http.Client
}

func NewHTTPClient(rawBaseURL string, supplied *http.Client) (*HTTPClient, error) {
	base, err := parseHTTPSBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	client := supplied
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout: 90 * time.Second,
		}, Timeout: 2 * time.Minute}
	} else {
		copy := *client
		client = &copy
	}
	if client.Timeout <= 0 {
		client.Timeout = 2 * time.Minute
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPClient{base: base, client: client}, nil
}

func (c *HTTPClient) RequestOTP(ctx context.Context, phone string) error {
	return c.apiJSON(ctx, http.MethodPost, requestOTPPath, AuthSession{}, map[string]string{"phone": phone}, nil)
}
func (c *HTTPClient) VerifyOTP(ctx context.Context, phone, otp string) (AuthSession, error) {
	var out AuthSession
	err := c.apiJSON(ctx, http.MethodPost, verifyOTPPath, AuthSession{}, map[string]string{"phone": phone, "otp": otp}, &out)
	return out, err
}
func (c *HTTPClient) SubmitConsent(ctx context.Context, auth AuthSession, consent Consent) error {
	return c.apiJSON(ctx, http.MethodPost, consentPath, auth, consent, nil)
}
func (c *HTTPClient) CreateUpload(ctx context.Context, auth AuthSession, metadata UploadMetadata) (UploadTarget, error) {
	var out UploadTarget
	err := c.apiJSON(ctx, http.MethodPost, uploadsPath, auth, metadata, &out)
	if err == nil {
		if out.TaskID == "" {
			return UploadTarget{}, ErrInvalid
		}
		if _, err = validateUploadURL(out.URL); err != nil {
			return UploadTarget{}, err
		}
	}
	return out, err
}
func (c *HTTPClient) Upload(ctx context.Context, auth AuthSession, target UploadTarget, body io.Reader) error {
	_ = auth // Deliberately never forwarded to object storage.
	u, err := validateUploadURL(target.URL)
	if err != nil {
		return err
	}
	method := target.Method
	if method == "" {
		method = http.MethodPut
	}
	if method != http.MethodPut && method != http.MethodPost {
		return ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return ErrInvalid
	}
	for name, value := range target.Headers {
		lower := strings.ToLower(name)
		if !allowedUploadHeaders[lower] || strings.ContainsAny(value, "\r\n") {
			return ErrInvalid
		}
		req.Header.Set(name, value)
	}
	if raw := req.Header.Get("Content-Length"); raw != "" {
		length, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || length < 0 {
			return ErrInvalid
		}
		req.ContentLength = length
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: upload transport", ErrRetryable)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyResponse(resp)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
	return err
}
func (c *HTTPClient) CompleteUpload(ctx context.Context, auth AuthSession, taskID string, digest Digest) (Task, error) {
	var out Task
	err := c.apiJSON(ctx, http.MethodPost, uploadsPath+"/"+url.PathEscape(taskID)+"/complete", auth, digest, &out)
	return out, err
}
func (c *HTTPClient) GetTask(ctx context.Context, auth AuthSession, taskID string) (Task, error) {
	var out Task
	err := c.apiJSON(ctx, http.MethodGet, tasksPath+url.PathEscape(taskID), auth, nil, &out)
	return out, err
}
func (c *HTTPClient) DownloadPoster(ctx context.Context, auth AuthSession, taskID string) (Poster, error) {
	req, err := c.apiRequest(ctx, http.MethodGet, tasksPath+url.PathEscape(taskID)+"/poster", auth, nil)
	if err != nil {
		return Poster{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Poster{}, ctxErr
		}
		return Poster{}, fmt.Errorf("%w: poster transport", ErrRetryable)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return Poster{}, classifyResponse(resp)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/svg+xml") {
		resp.Body.Close()
		return Poster{}, ErrUnsupportedMedia
	}
	if resp.ContentLength > maxPosterBytes {
		resp.Body.Close()
		return Poster{}, ErrResponseTooLarge
	}
	return Poster{Body: &posterReadCloser{body: resp.Body, remaining: maxPosterBytes},
		ContentType: mediaType, ContentLength: resp.ContentLength}, nil
}

func (c *HTTPClient) apiJSON(ctx context.Context, method, path string, auth AuthSession, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return ErrInvalid
		}
		body = bytes.NewReader(data)
	}
	req, err := c.apiRequest(ctx, method, path, auth, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: api transport", ErrRetryable)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyResponse(resp)
	}
	if output == nil {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if readErr != nil {
			return fmt.Errorf("%w: invalid response", ErrRemote)
		}
		if len(data) > maxResponseBytes {
			return ErrResponseTooLarge
		}
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w: invalid response", ErrRemote)
	}
	if len(data) > maxResponseBytes {
		return ErrResponseTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("%w: invalid response", ErrRemote)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%w: invalid response", ErrRemote)
	}
	return nil
}
func (c *HTTPClient) apiRequest(ctx context.Context, method, path string, auth AuthSession, body io.Reader) (*http.Request, error) {
	u := *c.base
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	if auth.Bearer != "" {
		if strings.ContainsAny(auth.Bearer, "\r\n") {
			return nil, ErrUnauthenticated
		}
		req.Header.Set("Authorization", "Bearer "+auth.Bearer)
	}
	return req, nil
}
func parseHTTPSBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return nil, ErrInvalid
	}
	if !validPort(u) {
		return nil, ErrInvalid
	}
	u.Path = ""
	u.RawPath = ""
	return u, nil
}
func validateUploadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.Fragment != "" || u.Opaque != "" {
		return nil, ErrInvalid
	}
	if !validPort(u) {
		return nil, ErrInvalid
	}
	return u, nil
}

func validPort(u *url.URL) bool {
	port := u.Port()
	if port == "" {
		return !strings.HasSuffix(u.Host, ":")
	}
	value, err := strconv.Atoi(port)
	return err == nil && value >= 1 && value <= 65535
}
func classifyResponse(resp *http.Response) error {
	var payload struct {
		Code string `json:"code"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes+1))
	if err != nil {
		return ErrRemote
	}
	if len(data) > maxErrorBytes {
		return &Error{Kind: ErrResponseTooLarge, StatusCode: resp.StatusCode}
	}
	_ = json.Unmarshal(data, &payload)
	kind := ErrRemote
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = ErrUnauthenticated
	case http.StatusNotFound:
		kind = ErrNotFound
	case http.StatusConflict:
		kind = ErrConflict
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		kind = ErrRetryable
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = ErrInvalid
	default:
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 && resp.StatusCode != http.StatusNotImplemented {
			kind = ErrRetryable
		}
	}
	return &Error{Kind: kind, StatusCode: resp.StatusCode, Code: safeCode(payload.Code)}
}
func safeCode(code string) string {
	if len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return code
}

type posterReadCloser struct {
	body      io.ReadCloser
	remaining int64
	closed    bool
}

func (r *posterReadCloser) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if r.remaining > 0 {
		if int64(len(p)) > r.remaining {
			p = p[:r.remaining]
		}
		n, err := r.body.Read(p)
		r.remaining -= int64(n)
		return n, err
	}
	var probe [1]byte
	n, err := r.body.Read(probe[:])
	if n > 0 {
		_ = r.Close()
		return 0, ErrResponseTooLarge
	}
	return 0, err
}

func (r *posterReadCloser) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.body.Close()
}

var _ Client = (*HTTPClient)(nil)
var _ Client = (*MockClient)(nil)
