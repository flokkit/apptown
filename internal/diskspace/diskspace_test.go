package diskspace

import (
	"errors"
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

// New work is refused below the floor, max(1 GiB, 5% of the filesystem),
// and admitted at it; a filesystem not read is refused with the reason;
// a tenth free is only a warning.
//
// Kills: the floor as 5% alone (a small disk admits work with 250 MiB
// free); as 1 GiB alone (a large disk admits with 1.1 GiB of 2 TiB); a
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
		{"a small disk below 1 GiB, though over 5%", Space{5 * gib, gib - 1}, true, false},
		{"a large disk at exactly 5%", Space{2000 * gib, 100 * gib}, false, true},
		{"a large disk below 5%, though over 1 GiB", Space{2000 * gib, 100*gib - 1}, true, true},
		{"a disk between 5% and 10%", Space{100 * gib, 7 * gib}, false, true},
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
