//go:build !windows

package copilot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const copilotFIFODeadline = 300 * time.Millisecond

func TestDiscoverRejectsFIFOReplacedAfterDirectoryEntryWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	body := `{"version":3,"sessionId":"fifo-swap",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic message"},` + syntheticAgent + `}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	hookDone := make(chan struct{})
	var replacementErr error
	a.beforeOpen = func(candidate string) {
		if candidate != path {
			return
		}
		if err := os.Remove(path); err != nil {
			replacementErr = err
		} else {
			replacementErr = unix.Mkfifo(path, 0o600)
		}
		close(hookDone)
	}
	type result struct {
		sessions int
		err      error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		sessions, err := a.Discover(ctx)
		done <- result{sessions: len(sessions), err: err}
	}()
	select {
	case <-hookDone:
	case <-time.After(time.Second):
		t.Fatal("replacement hook was not reached")
	}
	if replacementErr != nil {
		t.Fatal(replacementErr)
	}
	select {
	case got := <-done:
		if got.err != nil || got.sessions != 0 {
			t.Fatalf("sessions=%d err=%v", got.sessions, got.err)
		}
	case <-time.After(copilotFIFODeadline):
		writer, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			_ = unix.Close(writer)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatalf("Discover blocked on replacement FIFO; fallback writer err=%v", err)
	}
	a.mu.RLock()
	known := len(a.known)
	a.mu.RUnlock()
	if known != 0 {
		t.Fatalf("installed %d known references", known)
	}
}
