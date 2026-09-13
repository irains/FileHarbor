//go:build windows

package main

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// confirmedTrashCrossVolume only bypasses native directory moves when both
// existing directories can be inspected and their volume identities differ.
func confirmedTrashCrossVolume(source, destination string) bool {
	from, ok := trashDirectoryVolume(source)
	if !ok {
		return false
	}
	to, ok := trashDirectoryVolume(filepath.Dir(destination))
	return ok && from != to
}

func trashDirectoryVolume(path string) (uint32, bool) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return 0, false
	}
	return info.VolumeSerialNumber, true
}
