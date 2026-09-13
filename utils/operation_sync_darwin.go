//go:build darwin

package utils

// APFS and HFS+ reject fsync on directory descriptors. Audit-file data is
// flushed before rotation; metadata durability relies on the platform rename.
func syncOperationDirectory(string) error {
	return nil
}
