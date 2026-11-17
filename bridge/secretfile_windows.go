//go:build windows

package main

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const secretsEncrypted = true

func dpapiBlob(data []byte) *windows.DataBlob {
	if len(data) == 0 {
		return &windows.DataBlob{Size: 0, Data: new(byte)}
	}
	return &windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func takeDPAPIOutput(out *windows.DataBlob) []byte {
	if out.Data == nil {
		return []byte{}
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...)
}

func protectSecret(data []byte) ([]byte, error) {
	var out windows.DataBlob
	err := windows.CryptProtectData(dpapiBlob(data), nil, dpapiBlob(secretEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	return takeDPAPIOutput(&out), nil
}

func unprotectSecret(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, errors.New("empty DPAPI blob")
	}
	var out windows.DataBlob
	err := windows.CryptUnprotectData(dpapiBlob(blob), nil, dpapiBlob(secretEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	return takeDPAPIOutput(&out), nil
}

func restrictFile(string) {}

func restrictDir(string) {}
