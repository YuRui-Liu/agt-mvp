//go:build windows

package mocksvc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	windowsDACLInformation          = 0x00000004
	windowsOwnerInformation         = 0x00000001
	windowsProtectedDACLInformation = 0x80000000
	windowsUnprotectedDACLInfo      = 0x20000000
	windowsSEDACLProtected          = 0x1000
	windowsSEFileObject             = 1
	windowsSDDLRevision1            = 1
	windowsSecurityMaxSIDSize       = 68
	windowsTokenQuery               = 0x0008
	windowsTokenUserClass           = 1
	windowsAccessAllowedACEType     = 0
	windowsAccessAllowedCompoundACE = 4
	windowsAccessAllowedObjectACE   = 5
	windowsAccessAllowedCallbackACE = 9
	windowsAccessAllowedCallbackObj = 11
	windowsDangerousFileAccess      = 0xf00d0000
)

var (
	windowsAdvapi32                               = syscall.NewLazyDLL("advapi32.dll")
	windowsKernel32                               = syscall.NewLazyDLL("kernel32.dll")
	windowsConvertStringSecurityDescriptorToSDDLW = windowsAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	windowsSetFileSecurityW                       = windowsAdvapi32.NewProc("SetFileSecurityW")
	windowsGetNamedSecurityInfoW                  = windowsAdvapi32.NewProc("GetNamedSecurityInfoW")
	windowsGetSecurityDescriptorControl           = windowsAdvapi32.NewProc("GetSecurityDescriptorControl")
	windowsGetAce                                 = windowsAdvapi32.NewProc("GetAce")
	windowsEqualSid                               = windowsAdvapi32.NewProc("EqualSid")
	windowsGetLengthSid                           = windowsAdvapi32.NewProc("GetLengthSid")
	windowsCreateWellKnownSid                     = windowsAdvapi32.NewProc("CreateWellKnownSid")
	windowsOpenProcessToken                       = windowsAdvapi32.NewProc("OpenProcessToken")
	windowsGetTokenInformation                    = windowsAdvapi32.NewProc("GetTokenInformation")
	windowsGetCurrentProcess                      = windowsKernel32.NewProc("GetCurrentProcess")
	windowsLocalFree                              = windowsKernel32.NewProc("LocalFree")
)

type windowsACL struct {
	revision byte
	sbz1     byte
	size     uint16
	aceCount uint16
	sbz2     uint16
}

type windowsACEHeader struct {
	aceType  byte
	aceFlags byte
	aceSize  uint16
}

type windowsAccessAllowedACE struct {
	header   windowsACEHeader
	mask     uint32
	sidStart uint32
}

type windowsSIDAndAttributes struct {
	sid        unsafe.Pointer
	attributes uint32
}

type windowsTokenUser struct {
	user windowsSIDAndAttributes
}

func secureStoreDirectory(parent string) error {
	attributes, err := windowsFileAttributes(parent)
	switch {
	case err == nil:
		if err := validateWindowsStoreDirectory(attributes); err != nil {
			return err
		}
		return validateWindowsDirectoryACL(parent)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("inspect identity store directory: %w", err)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create identity store directory: %w", err)
	}
	attributes, err = windowsFileAttributes(parent)
	if err != nil {
		return fmt.Errorf("inspect new identity store directory: %w", err)
	}
	if err := validateWindowsStoreDirectory(attributes); err != nil {
		return err
	}
	const privateDirectorySDDL = "D:P(A;OICI;FA;;;OW)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if err := setWindowsDirectoryDACL(parent, privateDirectorySDDL); err != nil {
		return fmt.Errorf("secure new identity store directory ACL: %w", err)
	}
	return validateWindowsDirectoryACL(parent)
}

func windowsFileAttributes(path string) (uint32, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return syscall.GetFileAttributes(pointer)
}

func validateWindowsStoreDirectory(attributes uint32) error {
	if attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("identity store directory must not be a reparse point")
	}
	if attributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errors.New("identity store parent is not a directory")
	}
	return nil
}

func setWindowsDirectoryDACL(path, sddl string) error {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sddlPointer, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return err
	}
	var descriptor unsafe.Pointer
	result, _, callErr := windowsConvertStringSecurityDescriptorToSDDLW.Call(
		uintptr(unsafe.Pointer(sddlPointer)),
		windowsSDDLRevision1,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	if result == 0 {
		return windowsAPIError(callErr)
	}
	defer windowsLocalFree.Call(uintptr(descriptor))

	var control uint16
	var revision uint32
	result, _, callErr = windowsGetSecurityDescriptorControl.Call(
		uintptr(descriptor),
		uintptr(unsafe.Pointer(&control)),
		uintptr(unsafe.Pointer(&revision)),
	)
	if result == 0 {
		return windowsAPIError(callErr)
	}
	protectionInformation := uintptr(windowsUnprotectedDACLInfo)
	if control&windowsSEDACLProtected != 0 {
		protectionInformation = windowsProtectedDACLInformation
	}
	result, _, callErr = windowsSetFileSecurityW.Call(
		uintptr(unsafe.Pointer(pathPointer)),
		windowsDACLInformation|protectionInformation,
		uintptr(descriptor),
	)
	if result == 0 {
		return windowsAPIError(callErr)
	}
	return nil
}

func validateWindowsDirectoryACL(path string) error {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var dacl unsafe.Pointer
	var owner unsafe.Pointer
	var descriptor unsafe.Pointer
	result, _, _ := windowsGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(pathPointer)),
		windowsSEFileObject,
		windowsDACLInformation|windowsOwnerInformation,
		uintptr(unsafe.Pointer(&owner)),
		0,
		uintptr(unsafe.Pointer(&dacl)),
		0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 {
		return syscall.Errno(result)
	}
	if descriptor != nil {
		defer windowsLocalFree.Call(uintptr(descriptor))
	}
	if dacl == nil {
		return errors.New("identity store directory has an unrestricted DACL")
	}

	var control uint16
	var revision uint32
	result, _, callErr := windowsGetSecurityDescriptorControl.Call(
		uintptr(descriptor),
		uintptr(unsafe.Pointer(&control)),
		uintptr(unsafe.Pointer(&revision)),
	)
	if result == 0 {
		return windowsAPIError(callErr)
	}
	if control&windowsSEDACLProtected == 0 {
		return errors.New("identity store directory DACL must be protected")
	}
	if owner == nil {
		return errors.New("identity store directory has no owner SID")
	}

	ownerSID, err := copyWindowsSID(owner)
	if err != nil {
		return fmt.Errorf("read identity store owner SID: %w", err)
	}
	currentUserSID, err := currentWindowsUserSID()
	if err != nil {
		return fmt.Errorf("read current user SID: %w", err)
	}
	localSystemSID, err := createWindowsWellKnownSID(22)
	if err != nil {
		return err
	}
	administratorsSID, err := createWindowsWellKnownSID(26)
	if err != nil {
		return err
	}
	trustedOwnerSIDs := [][]byte{currentUserSID, localSystemSID, administratorsSID}
	if err := validateWindowsOwnerSID(ownerSID, trustedOwnerSIDs); err != nil {
		return err
	}
	ownerRightsSID, err := createWindowsWellKnownSID(71)
	if err != nil {
		return err
	}
	allowedSIDs := append(trustedOwnerSIDs, ownerRightsSID)

	acl := (*windowsACL)(dacl)
	for index := uint32(0); index < uint32(acl.aceCount); index++ {
		var ace unsafe.Pointer
		result, _, callErr := windowsGetAce.Call(
			uintptr(dacl),
			uintptr(index),
			uintptr(unsafe.Pointer(&ace)),
		)
		if result == 0 {
			return windowsAPIError(callErr)
		}
		if ace == nil {
			return errors.New("identity store directory contains a nil ACE")
		}
		header := (*windowsACEHeader)(ace)
		if !isWindowsAllowACEType(header.aceType) {
			continue
		}
		if header.aceType != windowsAccessAllowedACEType {
			return errors.New("identity store directory contains an unsupported allow ACE")
		}
		sidOffset := unsafe.Offsetof(windowsAccessAllowedACE{}.sidStart)
		if uintptr(header.aceSize) < sidOffset+8 {
			return errors.New("identity store directory contains a malformed allow ACE")
		}
		allowedACE := (*windowsAccessAllowedACE)(ace)
		sid := unsafe.Add(ace, sidOffset)
		sidLength, err := windowsSIDLength(sid)
		if err != nil || uintptr(header.aceSize) < sidOffset+uintptr(sidLength) {
			return errors.New("identity store directory contains a malformed allow SID")
		}
		if windowsSIDAllowed(sid, allowedSIDs) {
			continue
		}
		if allowedACE.mask&windowsDangerousFileAccess != 0 {
			return errors.New("identity store directory ACL grants dangerous access to an unapproved SID")
		}
		return errors.New("identity store directory ACL grants access to an unapproved SID")
	}
	return nil
}

func validateWindowsOwnerSID(owner []byte, trustedOwners [][]byte) error {
	for _, trustedOwner := range trustedOwners {
		if len(trustedOwner) != 0 && bytes.Equal(owner, trustedOwner) {
			return nil
		}
	}
	return errors.New("identity store directory has an unapproved owner SID")
}

func isWindowsAllowACEType(aceType byte) bool {
	switch aceType {
	case windowsAccessAllowedACEType,
		windowsAccessAllowedCompoundACE,
		windowsAccessAllowedObjectACE,
		windowsAccessAllowedCallbackACE,
		windowsAccessAllowedCallbackObj:
		return true
	default:
		return false
	}
}

func windowsSIDAllowed(sid unsafe.Pointer, allowed [][]byte) bool {
	for _, candidate := range allowed {
		if len(candidate) == 0 {
			continue
		}
		result, _, _ := windowsEqualSid.Call(
			uintptr(sid),
			uintptr(unsafe.Pointer(&candidate[0])),
		)
		if result != 0 {
			return true
		}
	}
	return false
}

func currentWindowsUserSID() ([]byte, error) {
	process, _, callErr := windowsGetCurrentProcess.Call()
	if process == 0 {
		return nil, windowsAPIError(callErr)
	}
	var token syscall.Handle
	result, _, callErr := windowsOpenProcessToken.Call(
		process,
		windowsTokenQuery,
		uintptr(unsafe.Pointer(&token)),
	)
	if result == 0 {
		return nil, windowsAPIError(callErr)
	}
	defer syscall.CloseHandle(token)

	var size uint32
	result, _, callErr = windowsGetTokenInformation.Call(
		uintptr(token),
		windowsTokenUserClass,
		0,
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if result != 0 || !errors.Is(callErr, syscall.ERROR_INSUFFICIENT_BUFFER) {
		return nil, windowsAPIError(callErr)
	}
	buffer := make([]byte, size)
	result, _, callErr = windowsGetTokenInformation.Call(
		uintptr(token),
		windowsTokenUserClass,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(size),
		uintptr(unsafe.Pointer(&size)),
	)
	if result == 0 {
		return nil, windowsAPIError(callErr)
	}
	return copyWindowsSID((*windowsTokenUser)(unsafe.Pointer(&buffer[0])).user.sid)
}

func copyWindowsSID(sid unsafe.Pointer) ([]byte, error) {
	length, err := windowsSIDLength(sid)
	if err != nil {
		return nil, err
	}
	result := make([]byte, length)
	copy(result, unsafe.Slice((*byte)(sid), int(length)))
	return result, nil
}

func windowsSIDLength(sid unsafe.Pointer) (uint32, error) {
	if sid == nil {
		return 0, errors.New("nil SID")
	}
	length, _, callErr := windowsGetLengthSid.Call(uintptr(sid))
	if length < 8 || length > windowsSecurityMaxSIDSize {
		return 0, windowsAPIError(callErr)
	}
	return uint32(length), nil
}

func createWindowsWellKnownSID(sidType uint32) ([]byte, error) {
	sid := make([]byte, windowsSecurityMaxSIDSize)
	size := uint32(len(sid))
	result, _, callErr := windowsCreateWellKnownSid.Call(
		uintptr(sidType),
		0,
		uintptr(unsafe.Pointer(&sid[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if result == 0 {
		return nil, windowsAPIError(callErr)
	}
	return sid[:size], nil
}

func windowsAPIError(callErr error) error {
	if callErr != nil && callErr != syscall.Errno(0) {
		return callErr
	}
	return syscall.EINVAL
}
