//go:build !windows

package main

import (
	"errors"
	"os"
)

const secretsEncrypted = false

func protectSecret([]byte) ([]byte, error) {
	return nil, errors.ErrUnsupported
}

func unprotectSecret([]byte) ([]byte, error) {
	return nil, errors.ErrUnsupported
}

func restrictFile(path string) {
	_ = os.Chmod(path, 0o600)
}

func restrictDir(path string) {
	_ = os.Chmod(path, 0o700)
}
