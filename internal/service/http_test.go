package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewHTTPClientRejectsUnsafeBaseURLs(t *testing.T) {
	for _, raw := range []string{
		"http://example.com", "https://u:p@example.com", "https://example.com?q=1",
		"https://example.com/#x", "mailto:test@example.com", "https://example.com/api",
		"https://example.com:0", "https://example.com:65536", "https://example.com:abc",
	} {
		if _, err := NewHTTPClient(raw, nil); err == nil {
			t.Errorf("NewHTTPClient(%q) error = nil", raw)
		}
	}
}

func TestHTTPPosterValidationAndErrorLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp *http.Response
		want error
	}{
		{"unknown type", response(http.StatusOK, "text/html", []byte("x")), ErrUnsupportedMedia},
		{"oversize", &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(strings.NewReader("")), ContentLength: maxPosterBytes + 1}, ErrResponseTooLarge},
		{"error body", response(http.StatusBadRequest, "text/plain", []byte(strings.Repeat("x", maxErrorBytes+1))), ErrResponseTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := NewHTTPClient("https://api.example", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return tc.resp, nil })})
			_, err := client.DownloadPoster(context.Background(), AuthSession{Bearer: "secret"}, "task")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error { b.closed = true; return nil }

func TestHTTPPosterAllowsUnknownLengthAndDetectsStreamingOverflow(t *testing.T) {
	t.Run("unknown length success", func(t *testing.T) {
		source := &trackingBody{Reader: strings.NewReader("png")}
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/png"}}, Body: source, ContentLength: -1}
		client, _ := NewHTTPClient("https://api.example", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return resp, nil })})
		poster, err := client.DownloadPoster(context.Background(), AuthSession{}, "task")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(poster.Body)
		if err != nil || string(data) != "png" {
			t.Fatalf("ReadAll = %q, %v", data, err)
		}
		if err := poster.Body.Close(); err != nil || !source.closed {
			t.Fatalf("Close = %v, source.closed=%v", err, source.closed)
		}
	})
	t.Run("unknown length overflow", func(t *testing.T) {
		source := &trackingBody{Reader: io.LimitReader(zeroReader{}, maxPosterBytes+1)}
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/png"}}, Body: source, ContentLength: -1}
		client, _ := NewHTTPClient("https://api.example", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return resp, nil })})
		poster, err := client.DownloadPoster(context.Background(), AuthSession{}, "task")
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, poster.Body)
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("stream error = %v", err)
		}
		if !source.closed {
			t.Fatal("overflow did not close source body")
		}
		if err := poster.Body.Close(); err != nil {
			t.Fatalf("second Close = %v", err)
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestHTTPStatusClassificationRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusRequestTimeout, ErrRetryable},
		{http.StatusTooManyRequests, ErrRetryable},
		{http.StatusInternalServerError, ErrRetryable},
		{http.StatusBadGateway, ErrRetryable},
		{http.StatusServiceUnavailable, ErrRetryable},
		{http.StatusGatewayTimeout, ErrRetryable},
		{http.StatusHTTPVersionNotSupported, ErrRetryable},
		{http.StatusNotImplemented, ErrRemote},
		{http.StatusBadRequest, ErrInvalid},
		{http.StatusUnauthorized, ErrUnauthenticated},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusConflict, ErrConflict},
		{http.StatusTeapot, ErrRemote},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			err := classifyResponse(response(tc.status, "application/json", []byte(`{"code":"safe_code"}`)))
			if !errors.Is(err, tc.want) {
				t.Fatalf("classifyResponse(%d) = %v, want %v", tc.status, err, tc.want)
			}
		})
	}
}

func TestHTTPUploadRejectsUnsafeHeadersAndDoesNotRetryCanceledRequest(t *testing.T) {
	calls := 0
	client, _ := NewHTTPClient("https://api.example", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	target := UploadTarget{TaskID: "task", URL: "https://store.example/x", Headers: map[string]string{"Authorization": "Bearer leak"}}
	if err := client.Upload(context.Background(), AuthSession{Bearer: "secret"}, target, strings.NewReader("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe header error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target.Headers = nil
	err := client.Upload(ctx, AuthSession{Bearer: "secret"}, target, strings.NewReader("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d", calls)
	}
}

func TestHTTPClientUsesBearerOnlyForServiceAndSendsExactCompleteFields(t *testing.T) {
	var uploadAuthorization string
	var complete map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "store.example" {
			uploadAuthorization = r.Header.Get("Authorization")
			_, _ = io.Copy(io.Discard, r.Body)
			return response(http.StatusNoContent, "", nil), nil
		}
		if strings.Contains(r.URL.String(), "secret") {
			t.Errorf("bearer leaked into URL %q", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case uploadsPath:
			data, _ := json.Marshal(UploadTarget{TaskID: "task", URL: "https://store.example/object", Headers: map[string]string{"Content-Length": "5"}})
			return response(http.StatusOK, "application/json", data), nil
		case uploadsPath + "/task/complete":
			if err := json.NewDecoder(r.Body).Decode(&complete); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(Task{ID: "task", Status: StatusComplete})
			return response(http.StatusOK, "application/json", data), nil
		default:
			return response(http.StatusNotFound, "application/json", nil), nil
		}
	})

	client, err := NewHTTPClient("https://api.example", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	auth := AuthSession{Bearer: "secret"}
	target, err := client.CreateUpload(context.Background(), auth, UploadMetadata{IdempotencyKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Upload(context.Background(), auth, target, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	digest := Digest{SHA256: strings.Repeat("a", 64), Bytes: 5, Sessions: 2, SchemaVersion: "v2"}
	if _, err := client.CompleteUpload(context.Background(), auth, "task", digest); err != nil {
		t.Fatal(err)
	}
	if uploadAuthorization != "" {
		t.Fatalf("object storage Authorization = %q", uploadAuthorization)
	}
	if len(complete) != 4 || complete["digest"] != digest.SHA256 || complete["bytes"] != float64(5) ||
		complete["sessions"] != float64(2) || complete["schema_version"] != "v2" {
		t.Fatalf("complete payload = %#v", complete)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, contentType string, body []byte) *http.Response {
	header := make(http.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
	}
}

func TestHTTPClientRejectsRedirects(t *testing.T) {
	c, err := NewHTTPClient("https://example.com", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.client.CheckRedirect(nil, nil); err == nil {
		t.Fatal("CheckRedirect error = nil")
	}
}
