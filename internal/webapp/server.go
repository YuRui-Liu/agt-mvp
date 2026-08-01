package webapp

import (
	"embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed assets/*
var assets embed.FS

const launchCookieName = "kuai_launch"

var securityHeaders = map[string]string{
	"Cache-Control":           "no-store",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "no-referrer",
	"Content-Security-Policy": "default-src 'self'; img-src 'self' blob: data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
}

func writeAsset(w http.ResponseWriter, name, contentType string) {
	body, err := assets.ReadFile("assets/" + name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "asset_unavailable", "页面资源暂时不可用")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeIndexAsset(w http.ResponseWriter, mode, host string) {
	body, err := assets.ReadFile("assets/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "asset_unavailable", "页面资源暂时不可用")
		return
	}
	if mode == "" {
		mode = "mock"
	}
	bootstrap, _ := json.Marshal(struct {
		ServiceMode string `json:"service_mode"`
		ServiceHost string `json:"service_host,omitempty"`
	}{mode, host})
	page := strings.Replace(string(body), "{{BOOTSTRAP_JSON}}", string(bootstrap), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

func NewServer(address string, app *App) (*http.Server, net.Listener, error) {
	if err := validateApp(app); err != nil {
		return nil, nil, err
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || !isLoopbackHost(host) {
		return nil, nil, errors.New("server address must be loopback")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, errors.New("local server could not start")
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		_ = listener.Close()
		return nil, nil, errors.New("server address must be loopback")
	}
	server := &http.Server{Handler: Handler(app), ReadHeaderTimeout: 15 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	server.RegisterOnShutdown(app.Close)
	return server, listener, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
