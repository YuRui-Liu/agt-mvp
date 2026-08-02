//go:build windows

package upload

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func createArtifactBacking(tempDir string) (*os.File, func() error, error) {
	dir, err := os.MkdirTemp(tempDir, ".kuai-upload-*")
	if err != nil {
		return nil, nil, errors.New("upload: temporary package unavailable")
	}
	path := filepath.Join(dir, "package.json")
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		_ = os.Remove(dir)
		return nil, nil, errors.New("upload: temporary package unavailable")
	}
	handle, err := windows.CreateFile(
		pathUTF16,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
		nil,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE,
		0,
	)
	if err != nil {
		_ = os.Remove(dir)
		return nil, nil, errors.New("upload: temporary package unavailable")
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		_ = os.Remove(dir)
		return nil, nil, errors.New("upload: temporary package unavailable")
	}
	cleanup := func() error {
		closeErr := file.Close()
		_ = os.Remove(dir)
		return closeErr
	}
	return file, cleanup, nil
}
