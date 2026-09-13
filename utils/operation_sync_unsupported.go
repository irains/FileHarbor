//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package utils

func syncOperationDirectory(string) error {
	return nil
}
