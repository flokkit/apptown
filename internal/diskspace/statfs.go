package diskspace

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Statfs reads the filesystem holding path, as this process sees it: its
// size and the space available to an unprivileged writer (what is reserved
// for root is not counted as free).
func Statfs(path string) (Space, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Space{}, fmt.Errorf("reading the filesystem of %s: %w", path, err)
	}
	bsize := int64(st.Bsize) // uint32 on macOS, int64 on Linux
	s := Space{TotalBytes: int64(st.Blocks) * bsize, FreeBytes: int64(st.Bavail) * bsize}
	if s.TotalBytes <= 0 {
		return Space{}, fmt.Errorf("reading the filesystem of %s: it reports no size", path)
	}
	return s, nil
}
