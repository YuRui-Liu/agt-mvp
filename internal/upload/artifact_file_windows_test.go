//go:build windows

package upload

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsArtifactBackingDeletesOnClose(t *testing.T) {
	root := t.TempDir()
	file, cleanup, err := createArtifactBacking(root)
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	if _, err := file.WriteString("private package"); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete-on-close backing remains: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private backing directory remains: entries=%v err=%v", entries, err)
	}
}

func TestWindowsArtifactBackingRequiresDeleteAndDeniesExternalReads(t *testing.T) {
	if artifactDesiredAccess&windows.DELETE == 0 {
		t.Fatal("delete-on-close backing lacks DELETE access")
	}
	if artifactShareMode != windows.FILE_SHARE_DELETE {
		t.Fatalf("artifact share mode = %#x, want delete-only", artifactShareMode)
	}
}
