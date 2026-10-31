package diskspace

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

// df's one filesystem line is read as total and free, in bytes, among
// whatever else the command printed; a df that read nothing is an error,
// never an empty disk.
func TestTheDiskIsReadAsDFReportsIt(t *testing.T) {
	t.Parallel()
	d, err := ParseDF([]byte("Unable to find image 'busybox' locally\nFilesystem           1024-blocks    Used Available Capacity Mounted on\nrpool/docker         754000000 50000000 704000000   7% /d\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.TotalBytes != 754000000<<10 || d.FreeBytes != 704000000<<10 {
		t.Fatalf("disk %+v", d)
	}
	for _, bad := range []string{"df: /d: No such file or directory\n", "Filesystem 1024-blocks Used Available Capacity Mounted on\nx 0 0 0 0% /d\n"} {
		if _, err := ParseDF([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// New work is refused below the floor, 5% of the filesystem but at least
// 1 GiB and at most 20 GiB, and admitted at it; a filesystem not read is refused with the reason;
// advisory warnings are capped and never fall below the refusal floor.
//
// Kills: the floor as 5% alone (a small disk admits work with 250 MiB
// free); as 1 GiB alone (a large disk admits with 1.1 GiB of 2 TiB); the
// 20 GiB cap removed (a 4 TiB disk refuses work with 100 GiB free); a
// refusal at exactly the floor; an unread disk admitted.
func TestNewWorkIsRefusedBelowTheFloorAndWhenUnread(t *testing.T) {
	t.Parallel()
	const gib = int64(1) << 30
	for _, c := range []struct {
		name         string
		s            Space
		refused, low bool
	}{
		{"a small disk above 1 GiB", Space{5 * gib, gib + 1}, false, false},
		{"a small disk at exactly 1 GiB", Space{5 * gib, gib}, false, false},
		{"a small disk below 1 GiB, though over 5%", Space{5 * gib, gib - 1}, true, true},
		{"a mid-size disk at exactly 5%", Space{200 * gib, 10 * gib}, false, true},
		{"a mid-size disk below 5%, though over 1 GiB", Space{200 * gib, 10*gib - 1}, true, true},
		{"a large disk with 20 GiB free, under 5%", Space{4096 * gib, 20 * gib}, false, true},
		{"a large disk below 20 GiB, though over 1 GiB", Space{4096 * gib, 20*gib - 1}, true, true},
		{"a disk between 5% and 10%", Space{100 * gib, 7 * gib}, false, true},
		{"large disk with 40 GiB free", Space{4096 * gib, 40 * gib}, false, true},
		{"large disk below 40 GiB", Space{4096 * gib, 40*gib - 1}, false, true},
		{"large disk at three percent free", Space{4096 * gib, 123 * gib}, false, true},
		{"mid-size disk at warning threshold", Space{200 * gib, 20 * gib}, false, false},
		{"mid-size disk below warning threshold", Space{200 * gib, 20*gib - 1}, false, true},
		{"huge healthy disk without arithmetic overflow", Space{math.MaxInt64, math.MaxInt64}, false, false},
	} {
		err := Admit("the disk", c.s, nil)
		if refused := errors.Is(err, ErrRefused); refused != c.refused || c.s.Refuses() != c.refused || c.s.Low() != c.low {
			t.Errorf("%s: refused %v (%v), low %v; want refused %v, low %v", c.name, refused, err, c.s.Low(), c.refused, c.low)
		}
		if c.refused && !strings.Contains(err.Error(), "free of") {
			t.Errorf("%s: the refusal does not say what is free: %v", c.name, err)
		}
	}
	err := Admit("the worker's state folder", Space{}, errors.New("permission denied"))
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("an unread disk: %v", err)
	}
	// Adding: what is written must still leave the floor free.
	if err := AdmitAdding("the disk", Space{100 * gib, 10 * gib}, nil, 5*gib); err != nil {
		t.Errorf("5 GiB onto 10 GiB free of 100 GiB was refused: %v", err)
	}
	if err := AdmitAdding("the disk", Space{100 * gib, 10 * gib}, nil, 5*gib+1); !errors.Is(err, ErrRefused) {
		t.Errorf("writing past the floor was admitted: %v", err)
	}
}

// The filesystem this process writes to is read as it is: a size, and
// what is free within it.
func TestTheFilesystemOfAFolderIsRead(t *testing.T) {
	t.Parallel()
	s, err := Statfs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalBytes <= 0 || s.FreeBytes < 0 || s.FreeBytes > s.TotalBytes {
		t.Errorf("read as %+v", s)
	}
	if _, err := Statfs(os.DevNull + "/absent"); err == nil {
		t.Error("an absent folder was read")
	}
}
