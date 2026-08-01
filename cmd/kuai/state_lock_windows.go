//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type fileStateLock struct {
	handle syscall.Handle
}

func acquireStateLock(path string) (stateLock, error) {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, errors.New("state lock unavailable")
	}
	parentName, err := syscall.UTF16PtrFromString(parent)
	if err != nil {
		return nil, errors.New("state lock unavailable")
	}
	parentAttributes, err := syscall.GetFileAttributes(parentName)
	if err != nil || parentAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		parentAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, errors.New("state lock unavailable")
	}

	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, errors.New("state lock unavailable")
	}
	handle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, errors.New("state lock unavailable")
	}
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &information); err != nil ||
		information.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		information.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("state lock unavailable")
	}
	return &fileStateLock{handle: handle}, nil
}

func (l *fileStateLock) Close() error {
	if l == nil || l.handle == syscall.InvalidHandle {
		return nil
	}
	err := syscall.CloseHandle(l.handle)
	l.handle = syscall.InvalidHandle
	return err
}
