//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireStateLockIsExclusiveAndReleasable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json.lock")
	first, err := acquireStateLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireStateLock(path); err == nil {
		t.Fatal("second state lock unexpectedly succeeded")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireStateLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireStateLockCreatesNestedDirectoryAndRejectsReparsePoints(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested", "kuai")
	lock, err := acquireStateLock(filepath.Join(nested, "state.json.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if _, err := acquireStateLock(filepath.Join(linkedParent, "state.json.lock")); err == nil {
		t.Fatal("reparse-point parent unexpectedly accepted")
	}
}
