package safeopen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Identity is a stable filesystem object identity. Its representation is
// platform-neutral so adapters can retain it without retaining open handles.
type Identity struct{ Volume, File uint64 }

// BoundRoot owns a handle to an already-validated root directory. Every
// operation is relative to that handle, not a later path lookup of the root.
type BoundRoot struct {
	mu       sync.RWMutex
	state    boundRootState
	identity Identity
	closed   bool
}

func Bind(root string) (*BoundRoot, error) {
	canonical, err := canonicalSystemPrefix(root)
	if err != nil {
		return nil, err
	}
	state, identity, err := bindRoot(canonical)
	if err != nil {
		return nil, err
	}
	return &BoundRoot{state: state, identity: identity}, nil
}

func (b *BoundRoot) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return closeBoundRoot(&b.state)
}

func (b *BoundRoot) Identity() Identity {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.identity
}

func (b *BoundRoot) ReadDir(relative string) ([]os.DirEntry, error) {
	relative, err := validRelative(relative)
	if err != nil {
		return nil, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return nil, errors.New("safeopen: bound root closed")
	}
	return readDirBound(&b.state, relative)
}

func (b *BoundRoot) Open(relative string, maxBytes int64) (*os.File, error) {
	relative, err := validRelative(relative)
	if err != nil || relative == "." {
		return nil, errors.New("safeopen: invalid relative file")
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return nil, errors.New("safeopen: bound root closed")
	}
	file, err := openFileBound(&b.state, relative)
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

// PathIdentity returns the bound identities for root and every directory in
// relative. It never resolves relative against the process namespace.
func (b *BoundRoot) PathIdentity(relative string) ([]Identity, error) {
	relative, err := validRelative(relative)
	if err != nil {
		return nil, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return nil, errors.New("safeopen: bound root closed")
	}
	return pathIdentityBound(&b.state, relative, b.identity)
}

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
	bound, err := Bind(filepath.Clean(absoluteRoot))
	if err != nil {
		return nil, err
	}
	defer bound.Close()
	return bound.Open(relative, maxBytes)
}

func validRelative(relative string) (string, error) {
	if relative == "" {
		relative = "."
	}
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("safeopen: invalid relative path")
	}
	return relative, nil
}

// canonicalSystemPrefix resolves only the first absolute component. This
// accepts operating-system aliases such as macOS /var -> /private/var while
// leaving every user-controlled descendant for handle-by-handle validation.
func canonicalSystemPrefix(root string) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return "", errors.New("safeopen: invalid root")
	}
	volume := filepath.VolumeName(root)
	prefix := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(root, prefix)
	parts := strings.Split(remainder, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" {
		return root, nil
	}
	first := filepath.Join(prefix, parts[0])
	resolved, err := filepath.EvalSymlinks(first)
	if err != nil {
		if os.IsNotExist(err) {
			return root, nil
		}
		return "", err
	}
	current := resolved
	for _, part := range parts[1:] {
		current = filepath.Join(current, part)
	}
	return filepath.Clean(current), nil
}
