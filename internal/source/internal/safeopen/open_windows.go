//go:build windows

package safeopen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type boundRootState struct {
	handle windows.Handle
	name   string
}

func bindRoot(root string) (boundRootState, Identity, error) {
	volume := filepath.VolumeName(root)
	if volume == "" {
		return boundRootState{}, Identity{}, errors.New("safeopen: missing volume")
	}
	volumeRoot := volume + string(filepath.Separator)
	name, err := windows.UTF16PtrFromString(volumeRoot)
	if err != nil {
		return boundRootState{}, Identity{}, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return boundRootState{}, Identity{}, err
	}
	currentName := volumeRoot
	relative := strings.TrimPrefix(root, volumeRoot)
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, err := windowsOpenAt(handle, part, true)
		if err != nil {
			windows.CloseHandle(handle)
			return boundRootState{}, Identity{}, err
		}
		windows.CloseHandle(handle)
		handle = next
		currentName = filepath.Join(currentName, part)
	}
	identity, err := windowsHandleIdentity(handle)
	if err != nil {
		windows.CloseHandle(handle)
		return boundRootState{}, Identity{}, err
	}
	return boundRootState{handle: handle, name: currentName}, identity, nil
}

func closeBoundRoot(state *boundRootState) error { return windows.CloseHandle(state.handle) }

func duplicateWindowsHandle(handle windows.Handle) (windows.Handle, error) {
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	err := windows.DuplicateHandle(process, handle, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS)
	return duplicate, err
}

func windowsOpenAt(root windows.Handle, part string, directory bool) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(part)
	if err != nil {
		return 0, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{RootDirectory: root, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	options := uint32(windows.FILE_OPEN_REPARSE_POINT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE, oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		return 0, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return 0, errors.New("safeopen: reparse point rejected")
	}
	return handle, nil
}

func windowsOpenBound(state *boundRootState, relative string, finalDirectory bool) (windows.Handle, string, error) {
	current, err := duplicateWindowsHandle(state.handle)
	if err != nil {
		return 0, "", err
	}
	name := state.name
	if relative == "." {
		return current, name, nil
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		next, err := windowsOpenAt(current, part, index < len(parts)-1 || finalDirectory)
		if err != nil {
			windows.CloseHandle(current)
			return 0, "", err
		}
		windows.CloseHandle(current)
		current = next
		name = filepath.Join(name, part)
	}
	return current, name, nil
}

func readDirBound(state *boundRootState, relative string) ([]os.DirEntry, error) {
	handle, name, err := windowsOpenBound(state, relative, true)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	defer file.Close()
	return file.ReadDir(-1)
}

func openFileBound(state *boundRootState, relative string) (*os.File, error) {
	handle, name, err := windowsOpenBound(state, relative, false)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

func pathIdentityBound(state *boundRootState, relative string, root Identity) ([]Identity, error) {
	identities := []Identity{root}
	if relative == "." {
		return identities, nil
	}
	current, err := duplicateWindowsHandle(state.handle)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		next, err := windowsOpenAt(current, part, true)
		if err != nil {
			windows.CloseHandle(current)
			return nil, err
		}
		windows.CloseHandle(current)
		current = next
		identity, err := windowsHandleIdentity(current)
		if err != nil {
			windows.CloseHandle(current)
			return nil, err
		}
		identities = append(identities, identity)
	}
	windows.CloseHandle(current)
	return identities, nil
}

func windowsHandleIdentity(handle windows.Handle) (Identity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return Identity{}, err
	}
	fileIndex := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return Identity{Volume: uint64(info.VolumeSerialNumber), File: fileIndex}, nil
}
