//go:build !windows

package safeopen

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type boundRootState struct{ file *os.File }

func bindRoot(root string) (boundRootState, Identity, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return boundRootState{}, Identity{}, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	parts := strings.Split(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		nextFD, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		if err != nil {
			current.Close()
			return boundRootState{}, Identity{}, err
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), part))
		current.Close()
		current = next
	}
	identity, err := unixFileIdentity(current)
	if err != nil {
		current.Close()
		return boundRootState{}, Identity{}, err
	}
	return boundRootState{file: current}, identity, nil
}

func closeBoundRoot(state *boundRootState) error { return state.file.Close() }

func duplicateRoot(state *boundRootState) (*os.File, error) {
	fd, err := unix.Dup(int(state.file.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), state.file.Name()), nil
}

func openFromBound(state *boundRootState, relative string, finalDirectory bool) (*os.File, error) {
	current, err := duplicateRoot(state)
	if err != nil {
		return nil, err
	}
	if relative == "." {
		return current, nil
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(parts)-1 || finalDirectory {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(current.Fd()), part, flags, 0)
		if err != nil {
			current.Close()
			return nil, err
		}
		next := os.NewFile(uintptr(fd), filepath.Join(current.Name(), part))
		current.Close()
		current = next
	}
	return current, nil
}

func readDirBound(state *boundRootState, relative string) ([]os.DirEntry, error) {
	dir, err := openFromBound(state, relative, true)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	var entries []os.DirEntry
	for {
		batch, err := dir.ReadDir(256)
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func readDirLimitBound(state *boundRootState, relative string, limit int) ([]os.DirEntry, error) {
	dir, err := openFromBound(state, relative, true)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, ErrDirectoryLimit
	}
	return entries, nil
}

func openFileBound(state *boundRootState, relative string) (*os.File, error) {
	return openFromBound(state, relative, false)
}

func openFileWithPathIdentityBound(state *boundRootState, relative string, root Identity) (*os.File, []Identity, error) {
	identities := []Identity{root}
	current, err := duplicateRoot(state)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		directory := index < len(parts)-1
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if directory {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(current.Fd()), part, flags, 0)
		if err != nil {
			current.Close()
			return nil, nil, err
		}
		next := os.NewFile(uintptr(fd), filepath.Join(current.Name(), part))
		current.Close()
		current = next
		if directory {
			identity, err := unixFileIdentity(current)
			if err != nil {
				current.Close()
				return nil, nil, err
			}
			identities = append(identities, identity)
		}
	}
	return current, identities, nil
}

func pathIdentityBound(state *boundRootState, relative string, root Identity) ([]Identity, error) {
	identities := []Identity{root}
	if relative == "." {
		return identities, nil
	}
	current, err := duplicateRoot(state)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		fd, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		if err != nil {
			current.Close()
			return nil, err
		}
		next := os.NewFile(uintptr(fd), filepath.Join(current.Name(), part))
		current.Close()
		current = next
		identity, err := unixFileIdentity(current)
		if err != nil {
			current.Close()
			return nil, err
		}
		identities = append(identities, identity)
	}
	current.Close()
	return identities, nil
}

func unixFileIdentity(file *os.File) (Identity, error) {
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return Identity{}, errors.New("safeopen: invalid directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("safeopen: directory identity unavailable")
	}
	return Identity{Volume: uint64(stat.Dev), File: uint64(stat.Ino)}, nil
}
