//go:build !windows

package cline

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFIFOAndUnreadableDecoysAreNeverOpened(t *testing.T) {
	manifest, messages := fixture(t)
	root := t.TempDir()
	installFixture(t, root, "session-alpha", manifest, messages)
	fifoDir := filepath.Join(root, "fifo-session")
	if err := os.Mkdir(fifoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(fifoDir, "fifo-session.json"), 0o600); err != nil {
		t.Skipf("fifo unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fifoDir, "fifo-session.messages.json"), messages, 0o600); err != nil {
		t.Fatal(err)
	}
	trap := filepath.Join(root, "db")
	if err := os.Mkdir(trap, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trap, "sentinel"), []byte("must not be opened"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(trap, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(trap, 0o700) })
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
