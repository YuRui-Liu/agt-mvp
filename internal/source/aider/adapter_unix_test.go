//go:build !windows

package aider

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFIFOAndUnreadableDecoysAreNeverOpened(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, fileName), 0o600); err != nil {
		t.Skipf("fifo unavailable: %v", err)
	}
	trap := filepath.Join(root, ".aider")
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
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
