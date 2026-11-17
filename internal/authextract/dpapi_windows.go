//go:build windows

package authextract

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiUnprotect decrypts data protected with the Windows Data Protection
// API for the current user. It runs in-process: the key never touches a
// temporary file or another program's command line.
func dpapiUnprotect(protected []byte) ([]byte, error) {
	if len(protected) == 0 {
		return nil, errors.New("empty DPAPI blob")
	}
	in := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := make([]byte, out.Size)
	copy(plain, unsafe.Slice(out.Data, out.Size))
	return plain, nil
}
