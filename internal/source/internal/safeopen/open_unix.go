//go:build !windows

package safeopen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openRelative(root, relative string) (*os.File, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(rootFD), root)
	info, err := current.Stat()
	if err != nil || !info.IsDir() {
		current.Close()
		return nil, errors.New("safeopen: root is not a directory")
	}
	parts := splitRelative(relative)
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index != len(parts)-1 {
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
		if afterOpenComponent != nil && index != len(parts)-1 {
			afterOpenComponent(index)
		}
	}
	return current, nil
}

func splitRelative(relative string) []string {
	return strings.Split(relative, string(filepath.Separator))
}
