//go:build !windows

package mocksvc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenFileStoreRejectsInsecureExistingDirectoryWithoutChmod(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir shared directory: %v", err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("chmod shared directory: %v", err)
	}
	if _, err := OpenFileStore(filepath.Join(parent, "state.json")); err == nil {
		t.Fatal("OpenFileStore accepted insecure existing directory")
	}
	assertPermission(t, parent, 0o755)
}

func TestOpenFileStoreRejectsSymlinkParent(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(base, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := OpenFileStore(filepath.Join(link, "state.json")); err == nil {
		t.Fatal("OpenFileStore accepted symlink parent")
	}
}

func TestOpenFileStoreCreatesPrivateDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "new-private")
	if _, err := OpenFileStore(filepath.Join(parent, "state.json")); err != nil {
		t.Fatalf("open file store: %v", err)
	}
	assertPermission(t, parent, 0o700)
}
