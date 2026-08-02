//go:build windows

package safeopen

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsOpenOptionsAreSynchronous(t *testing.T) {
	for _, directory := range []bool{false, true} {
		options := windowsOpenOptions(directory)
		if options&windows.FILE_SYNCHRONOUS_IO_NONALERT == 0 {
			t.Fatalf("directory=%v missing synchronous I/O option: %#x", directory, options)
		}
		if directory && options&windows.FILE_DIRECTORY_FILE == 0 {
			t.Fatalf("directory options missing directory flag: %#x", options)
		}
		if !directory && options&windows.FILE_NON_DIRECTORY_FILE == 0 {
			t.Fatalf("file options missing non-directory flag: %#x", options)
		}
	}
}
