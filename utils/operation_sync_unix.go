//go:build dragonfly || freebsd || linux || netbsd || openbsd

package utils

import "os"

func syncOperationDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
