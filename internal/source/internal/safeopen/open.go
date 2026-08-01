package safeopen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// afterOpenComponent is a deterministic test seam for path-swap coverage.
var afterOpenComponent func(int)

func Open(root, path string, maxBytes int64) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("safeopen: invalid path")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(filepath.Clean(absoluteRoot), path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == "." {
		return nil, errors.New("safeopen: path outside root")
	}
	file, err := openRelative(filepath.Clean(absoluteRoot), relative)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		file.Close()
		return nil, errors.New("safeopen: invalid source file")
	}
	return file, nil
}
