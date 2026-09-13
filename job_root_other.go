//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import "errors"

func jobRootIdentity(path string) (string, error) {
	return "", errors.New("stable root identity unsupported")
}
