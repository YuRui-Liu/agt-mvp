//go:build !windows

package upload

import (
	"errors"
	"os"
)

func createArtifactBacking(tempDir string) (*os.File, func() error, error) {
	file, err := os.CreateTemp(tempDir, ".kuai-upload-*.json")
	if err != nil {
		return nil, nil, errors.New("upload: temporary package unavailable")
	}
	path := file.Name()
	if err := os.Remove(path); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil, errors.New("upload: temporary package unavailable")
	}
	return file, file.Close, nil
}
