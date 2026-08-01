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

func openRelative(root, relative string) (*os.File, error) {
	parts := strings.Split(relative, string(filepath.Separator))
	handles := make([]windows.Handle, 0, len(parts)+1)
	closeHandles := func() {
		for _, handle := range handles {
			windows.CloseHandle(handle)
		}
	}
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	rootHandle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	handles = append(handles, rootHandle)
	var rootInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(rootHandle, &rootInfo); err != nil || rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		closeHandles()
		return nil, errors.New("safeopen: reparse point rejected")
	}
	for index, part := range parts {
		objectName, err := windows.NewNTUnicodeString(part)
		if err != nil {
			closeHandles()
			return nil, err
		}
		oa := &windows.OBJECT_ATTRIBUTES{RootDirectory: handles[len(handles)-1], ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
		oa.Length = uint32(unsafe.Sizeof(*oa))
		options := uint32(windows.FILE_OPEN_REPARSE_POINT)
		if index < len(parts)-1 {
			options |= windows.FILE_DIRECTORY_FILE
		} else {
			options |= windows.FILE_NON_DIRECTORY_FILE
		}
		var handle windows.Handle
		var iosb windows.IO_STATUS_BLOCK
		err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE, oa, &iosb, nil, 0,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options, 0, 0)
		if err != nil {
			closeHandles()
			return nil, err
		}
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
			info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			windows.CloseHandle(handle)
			closeHandles()
			return nil, errors.New("safeopen: reparse point rejected")
		}
		handles = append(handles, handle)
	}
	final := handles[len(handles)-1]
	handles = handles[:len(handles)-1]
	closeHandles()
	return os.NewFile(uintptr(final), filepath.Join(root, relative)), nil
}
