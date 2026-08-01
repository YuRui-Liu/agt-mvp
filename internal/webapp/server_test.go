package webapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLaunchCookieAndBearerRemainSeparate(t *testing.T) {
	handler := Handler(newTestApp())
	exchange := httptest.NewRequest(http.MethodGet, "/?token=launch-token", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, exchange)
	if response.Code != http.StatusSeeOther || len(response.Result().Cookies()) != 1 {
		t.Fatalf("exchange status=%d cookies=%v", response.Code, response.Result().Cookies())
	}
	cookie := response.Result().Cookies()[0]
	if cookie.Name != launchCookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie=%+v", cookie)
	}
	if strings.Contains(response.Body.String(), "launch-token") {
		t.Fatal("launch token leaked in response")
	}
	for _, path := range []string{"/api/tasks/latest", "/api/tasks/internal", "/api/tasks/internal/poster"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("submission-only route %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestProtectedRoutesRequireLoopbackLaunchAuthentication(t *testing.T) {
	handler := Handler(newTestApp())
	for _, path := range []string{"/", "/api/scopes", "/assets/app.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestNewServerRejectsNonLoopback(t *testing.T) {
	if _, _, err := NewServer("0.0.0.0:0", newTestApp()); err == nil {
		t.Fatal("accepted non-loopback listener")
	}
}
