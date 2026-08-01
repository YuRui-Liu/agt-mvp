//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type fileStateLock struct {
	file *os.File
}

func acquireStateLock(path string) (stateLock, error) {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, errors.New("state lock unavailable")
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("state lock unavailable")
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return nil, errors.New("state lock unavailable")
	}
	parentInfo, err = os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 ||
		parentInfo.Mode().Perm() != 0o700 {
		return nil, errors.New("state lock unavailable")
	}

	flags := os.O_CREATE | os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, errors.New("state lock unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("state lock unavailable")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, errors.New("state lock unavailable")
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("state lock unavailable")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("state lock unavailable")
	}
	return &fileStateLock{file: file}, nil
}

func (l *fileStateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
