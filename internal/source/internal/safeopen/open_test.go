package safeopen

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBindRejectsSymlinkedRootAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, "root"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	if bound, err := Bind(filepath.Join(linked, "root")); err == nil {
		bound.Close()
		t.Fatal("bound symlinked ancestor")
	}
}

func TestBoundRootStaysOnOriginalAfterAncestorSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix rename/symlink test")
	}
	base := t.TempDir()
	ancestor := filepath.Join(base, "ancestor")
	root := filepath.Join(ancestor, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "root"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "root", "session.jsonl"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "root", "external.jsonl"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	if err := os.Rename(ancestor, filepath.Join(base, "original-ancestor")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ancestor); err != nil {
		t.Fatal(err)
	}
	entries, err := bound.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "session.jsonl" {
		t.Fatalf("entries=%v", entryNames(entries))
	}
	file, err := bound.Open("session.jsonl", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "safe" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i := range entries {
		names[i] = entries[i].Name()
	}
	return names
}

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

func TestOpenRejectsSymlinkedRootAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	target := t.TempDir()
	root := filepath.Join(target, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	if file, err := Open(filepath.Join(linked, "root"), filepath.Join(linked, "root", "session.jsonl"), 1024); err == nil {
		file.Close()
		t.Fatal("symlinked root ancestor accepted")
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
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	if err := os.Rename(parent, filepath.Join(root, "original-parent")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	file, err := bound.Open(filepath.Join("original-parent", "session.jsonl"), 1024)
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
