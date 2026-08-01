package safeopen

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenRejectsSymlinkedParentAndFinalComponent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, filepath.Join(root, "linked-parent", "session.jsonl"), 1024); err == nil {
		t.Fatal("symlinked parent accepted")
	}
	if err := os.Symlink(filepath.Join(outside, "session.jsonl"), filepath.Join(root, "linked-final.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, filepath.Join(root, "linked-final.jsonl"), 1024); err == nil {
		t.Fatal("final symlink accepted")
	}
}

func TestOpenRejectsSymlinkRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	realRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(realRoot, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkParent := t.TempDir()
	linkedRoot := filepath.Join(linkParent, "root")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(linkedRoot, filepath.Join(linkedRoot, "session.jsonl"), 1024); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestOpenRemainsBoundWhenParentPathIsSwapped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix openat test")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	outside := t.TempDir()
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "session.jsonl"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "session.jsonl"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	afterOpenComponent = func(index int) {
		afterOpenComponent = nil
		if err := os.Rename(parent, filepath.Join(root, "original-parent")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, parent); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { afterOpenComponent = nil }()
	file, err := Open(root, filepath.Join(root, "parent", "session.jsonl"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "safe" {
		t.Fatalf("read %q through swapped path", data)
	}
}
