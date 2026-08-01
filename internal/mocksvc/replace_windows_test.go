//go:build windows

package mocksvc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceFileReplacesExistingDestination(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	if err := replaceFile(source, destination); err != nil {
		t.Fatalf("replace file: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("destination = %q, want new", got)
	}
}
