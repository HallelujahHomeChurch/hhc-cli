//go:build darwin && cgo

package auth

/*
#cgo LDFLAGS: -framework Security -framework Foundation -framework LocalAuthentication
#include <stdlib.h>
int hhc_keychain_read(const char*, void**, int*);
int hhc_keychain_save(const char*, const void*, int);
int hhc_keychain_delete(const char*);
void hhc_keychain_free(void*, int);
*/
import "C"

import "unsafe"

func nativeStoreError(status C.int) error {
	if status == 0 {
		return nil
	}
	if status == -25300 {
		return ErrCredentialNotFound
	}
	return ErrCredentialStoreUnavailable
}

func loadNativeSecret(profile string) ([]byte, error) {
	name := C.CString(profile)
	defer C.free(unsafe.Pointer(name))
	var data unsafe.Pointer
	var size C.int
	if err := nativeStoreError(C.hhc_keychain_read(name, &data, &size)); err != nil {
		return nil, err
	}
	defer C.hhc_keychain_free(data, size)
	return C.GoBytes(data, size), nil
}

func saveNativeSecret(profile string, data []byte) error {
	name := C.CString(profile)
	defer C.free(unsafe.Pointer(name))
	return nativeStoreError(C.hhc_keychain_save(name, unsafe.Pointer(&data[0]), C.int(len(data))))
}

func deleteNativeSecret(profile string) error {
	name := C.CString(profile)
	defer C.free(unsafe.Pointer(name))
	return nativeStoreError(C.hhc_keychain_delete(name))
}
