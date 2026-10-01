package auth

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	credentialDLL = windows.NewLazySystemDLL("advapi32.dll")
	credRead      = credentialDLL.NewProc("CredReadW")
	credWrite     = credentialDLL.NewProc("CredWriteW")
	credDelete    = credentialDLL.NewProc("CredDeleteW")
	credFree      = credentialDLL.NewProc("CredFree")
)

type nativeCredential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             windows.Filetime
	BlobSize                uint32
	Blob                    *byte
	Persist, AttributeCount uint32
	Attributes              unsafe.Pointer
	TargetAlias, UserName   *uint16
}

func credentialTarget(profile string) *uint16 {
	value, _ := windows.UTF16PtrFromString("HHC/cli/profiles/" + profile)
	return value
}

func nativeStoreError(err error) error {
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return ErrCredentialNotFound
	}
	return ErrCredentialStoreUnavailable
}

func loadNativeSecret(profile string) ([]byte, error) {
	var value *nativeCredential
	target := credentialTarget(profile)
	ok, _, err := credRead.Call(uintptr(unsafe.Pointer(target)), 1, 0, uintptr(unsafe.Pointer(&value)))
	runtime.KeepAlive(target)
	if ok == 0 {
		return nil, nativeStoreError(err)
	}
	defer credFree.Call(uintptr(unsafe.Pointer(value)))
	if value == nil || value.Type != 1 || value.BlobSize == 0 || value.BlobSize > 2560 || value.Blob == nil {
		return nil, ErrCredentialStoreUnavailable
	}
	data := unsafe.Slice(value.Blob, int(value.BlobSize))
	result := append([]byte(nil), data...)
	clear(data)
	return result, nil
}

func saveNativeSecret(profile string, data []byte) error {
	value := nativeCredential{Type: 1, TargetName: credentialTarget(profile), BlobSize: uint32(len(data)), Blob: &data[0], Persist: 2}
	ok, _, err := credWrite.Call(uintptr(unsafe.Pointer(&value)), 0)
	runtime.KeepAlive(value)
	runtime.KeepAlive(data)
	if ok == 0 {
		return nativeStoreError(err)
	}
	return nil
}

func deleteNativeSecret(profile string) error {
	target := credentialTarget(profile)
	ok, _, err := credDelete.Call(uintptr(unsafe.Pointer(target)), 1, 0)
	runtime.KeepAlive(target)
	if ok == 0 {
		return nativeStoreError(err)
	}
	return nil
}
