//go:build windows

package deviceidentity

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateIdentityFileMode(os.FileMode) error { return nil }

const cryptProtectUIForbidden = 0x1

type dataBlob struct {
	length uint32
	data   *byte
}

var (
	crypt32            = windows.NewLazySystemDLL("crypt32.dll")
	cryptProtectData   = crypt32.NewProc("CryptProtectData")
	cryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

func blobFromBytes(data []byte) dataBlob {
	if len(data) == 0 {
		return dataBlob{}
	}
	return dataBlob{length: uint32(len(data)), data: &data[0]}
}

func bytesFromBlob(blob dataBlob) []byte {
	if blob.length == 0 || blob.data == nil {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(blob.data, blob.length)...)
}

func protectPrivateKey(plain []byte) ([]byte, string, error) {
	in := blobFromBytes(plain)
	var out dataBlob
	result, _, callErr := cryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if result == 0 {
		return nil, "", fmt.Errorf("CryptProtectData: %w", callErr)
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.data))))
	return bytesFromBlob(out), "windows-dpapi-current-user", nil
}

func unprotectPrivateKey(ciphertext []byte, protection string) ([]byte, string, error) {
	if protection != "windows-dpapi-current-user" {
		return nil, "", errors.New("device identity is not protected with Windows DPAPI")
	}
	in := blobFromBytes(ciphertext)
	var out dataBlob
	result, _, callErr := cryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if result == 0 {
		return nil, "", fmt.Errorf("CryptUnprotectData: %w", callErr)
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(out.data))))
	return bytesFromBlob(out), protection, nil
}
