//go:build !windows

package main

func confirmedTrashCrossVolume(source, destination string) bool {
	return false
}
