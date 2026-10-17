package gittest

import "golang.org/x/sys/unix"

// copyTree clones the tree at src to dst on APFS (clonefile copies a folder
// whole, sharing blocks until either side writes), falling back to a plain
// copy on a file system that cannot clone.
func copyTree(src, dst string) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err == nil {
		return nil
	}
	return plainCopy(src, dst)
}
