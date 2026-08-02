package safeopen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestFileOpenSizeLimitSentinelIsExactAcrossAPIs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.json")
	if err := os.WriteFile(path, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()

	tests := []struct {
		name string
		open func(int64) (*os.File, error)
	}{
		{name: "Open", open: func(limit int64) (*os.File, error) { return Open(root, path, limit) }},
		{name: "BoundRoot.Open", open: func(limit int64) (*os.File, error) { return bound.Open("session.json", limit) }},
		{name: "BoundRoot.OpenWithPathIdentity", open: func(limit int64) (*os.File, error) {
			file, _, err := bound.OpenWithPathIdentity("session.json", limit)
			return file, err
		}},
	}
	for _, test := range tests {
		t.Run(test.name+" exact", func(t *testing.T) {
			file, err := test.open(4)
			if err != nil {
				t.Fatal(err)
			}
			file.Close()
		})
		t.Run(test.name+" plus one", func(t *testing.T) {
			file, err := test.open(3)
			if file != nil {
				file.Close()
				t.Fatal("oversized file returned")
			}
			if !errors.Is(err, ErrFileSizeLimit) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestFileSizeLimitSentinelDoesNotMatchNonSizeFailures(t *testing.T) {
	root := t.TempDir()
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if file, err := bound.Open("directory", 1); err == nil || errors.Is(err, ErrFileSizeLimit) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("directory error=%v", err)
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if file, err := Open(root, link, 1); err == nil || errors.Is(err, ErrFileSizeLimit) {
			if file != nil {
				file.Close()
			}
			t.Fatalf("symlink error=%v", err)
		}
	}
}

func TestReadDirLimitRejectsDirectoryOverLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 4097; i++ {
		name := filepath.Join(root, fmt.Sprintf("entry-%04d", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	entries, err := bound.ReadDirLimit(".", 4096)
	if err != ErrDirectoryLimit {
		t.Fatalf("err=%v", err)
	}
	if entries != nil {
		t.Fatalf("returned %d entries on limit error", len(entries))
	}
}

func TestReadDirLimitAcceptsExactLimit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	entries, err := bound.ReadDirLimit(".", 2)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}

func TestOpenWithPathIdentityRemainsBoundToOriginalTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics differ on Windows")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "session.json"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	file, identities, err := bound.OpenWithPathIdentity(filepath.Join("parent", "session.json"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Rename(parent, filepath.Join(root, "original-parent")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "session.json"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalIdentities, err := bound.PathIdentity("original-parent")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(identities, originalIdentities) {
		t.Fatalf("identities=%v original=%v", identities, originalIdentities)
	}
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "safe" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

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
