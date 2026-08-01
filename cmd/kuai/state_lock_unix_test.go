//go:build !windows

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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock permissions=%#o want 0600", info.Mode().Perm())
	}
}

func TestAcquireStateLockCreatesSecureNestedDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "nested", "kuai")
	lock, err := acquireStateLock(filepath.Join(parent, "state.json.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	info, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode=%v", info.Mode())
	}
}

func TestAcquireStateLockRejectsLockSymlinkWithoutChangingTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(parent, "state.json.lock")
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireStateLock(lockPath); err == nil {
		t.Fatal("symlink lock unexpectedly accepted")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("target permissions changed to %#o", info.Mode().Perm())
	}
}

func TestAcquireStateLockRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireStateLock(filepath.Join(linkedParent, "state.json.lock")); err == nil {
		t.Fatal("symlink parent unexpectedly accepted")
	}
}
