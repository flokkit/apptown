// Package diskspace reads how full a filesystem is and decides whether new
// disk-heavy work may start on it.
//
// The rule is a headroom guard, not a reservation: new work is refused while
// less than Floor is free (a twentieth of the filesystem, but at least
// 1 GiB and at most 20 GiB), and a filesystem that could not be read is refused too, with
// the reason. Work admitted above the floor can still fill the disk (two
// admissions at once, an image larger than it looked); what fails then fails
// as it always did. Low, a tenth free, is a warning only.
package diskspace

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Space is one filesystem: its size and what an unprivileged writer may
// still use, in bytes.
type Space struct {
	TotalBytes int64
	FreeBytes  int64
}

// MinFloor is the least free space new work is admitted with, however
// small the filesystem.
const MinFloor = 1 << 30

// MaxFloor caps the floor on a large filesystem: a twentieth of a 3.6 TiB
// disk is 186 GiB, far more than any one pull, build or task needs, and
// refusing work with that much free reads as a fault.
const MaxFloor = 20 << 30

// Floor is the free space below which new work is refused on s: a
// twentieth of the filesystem, but at least MinFloor and at most MaxFloor.
func (s Space) Floor() int64 { return max(MinFloor, min(MaxFloor, s.TotalBytes/20)) }

// Low says less than a tenth is free: a warning, not a refusal.
func (s Space) Low() bool { return s.FreeBytes*10 < s.TotalBytes }

// Refuses says new work is refused on s.
func (s Space) Refuses() bool { return s.FreeBytes < s.Floor() }

// ErrRefused is new work refused for disk space: too little free, or the
// filesystem could not be read. Nothing was started.
var ErrRefused = errors.New("refused for disk space")

// Admit says whether new work may start on the filesystem described by
// what ("the worker's state folder /var/lib/forge-worker"), read as s, or
// not read (err). A refusal wraps ErrRefused and says what is free and
// what to do.
func Admit(what string, s Space, err error) error {
	if err != nil {
		return fmt.Errorf("%w: %s could not be read, so new work is not started: %v", ErrRefused, what, err)
	}
	if s.Refuses() {
		return fmt.Errorf("%w: %s has %s free of %s; new work needs at least %s free. Remove what is no longer needed there (old images, stopped containers, released work) and try again",
			ErrRefused, what, Bytes(s.FreeBytes), Bytes(s.TotalBytes), Bytes(s.Floor()))
	}
	return nil
}

// AdmitAdding says whether size bytes more may be written to the filesystem
// read as s and still leave its floor free.
func AdmitAdding(what string, s Space, err error, size int64) error {
	if err != nil {
		return Admit(what, s, err)
	}
	if s.FreeBytes-size < s.Floor() {
		return fmt.Errorf("%w: %s has %s free of %s; writing %s more would leave less than %s free. Remove what is no longer needed there and try again",
			ErrRefused, what, Bytes(s.FreeBytes), Bytes(s.TotalBytes), Bytes(size), Bytes(s.Floor()))
	}
	return nil
}

// Bytes is n in binary units, to one decimal place: "26.7 GiB".
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// DFImage is what a read through Docker runs in: a small image pinned by
// digest (busybox 1.37.0), for its df.
const DFImage = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

// DockerDFArgs are the docker CLI's arguments that read the filesystem
// holding root on the daemon's machine (its data folder, say): a throwaway
// container of DFImage with no network mounts root read only and runs df
// there. Docker reports no disk figures itself, and docker system df walks
// every layer (minutes on a busy host). ParseDF reads the answer.
func DockerDFArgs(root string) []string {
	return []string{"run", "--rm", "--network", "none", "--read-only", "-v", root + ":/d:ro", DFImage, "df", "-Pk", "/d"}
}

// ParseDF reads df -Pk's filesystem line: total and available, in
// kibibytes. Other lines before it (a pull's progress, a notice) are left.
func ParseDF(out []byte) (Space, error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return Space{}, fmt.Errorf("df answered %q", strings.TrimSpace(string(out)))
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 6 {
		return Space{}, fmt.Errorf("df answered %q", lines[len(lines)-1])
	}
	total, err1 := strconv.ParseInt(f[1], 10, 64)
	avail, err2 := strconv.ParseInt(f[3], 10, 64)
	if err1 != nil || err2 != nil || total <= 0 || avail < 0 {
		return Space{}, fmt.Errorf("df answered %q", lines[len(lines)-1])
	}
	return Space{TotalBytes: total << 10, FreeBytes: avail << 10}, nil
}

// Reading is one filesystem as read at a time, for a person and the views
// that show it: what it holds and where, its size and free space, the
// floor below which new work there is refused, or why it could not be
// read. A filesystem not read is Unknown with the reason and refuses new
// work; it is never shown as empty or full.
type Reading struct {
	What       string    `json:"what"`
	Path       string    `json:"path,omitempty"`
	TotalBytes int64     `json:"totalBytes,omitempty"`
	FreeBytes  int64     `json:"freeBytes,omitempty"`
	FloorBytes int64     `json:"floorBytes,omitempty"`
	Refuses    bool      `json:"refuses"`
	Low        bool      `json:"low"`
	Unknown    string    `json:"unknown,omitempty"`
	ReadAt     time.Time `json:"readAt"`
}

// NewReading is what was read of the filesystem holding path at at: s, or
// err.
func NewReading(what, path string, s Space, err error, at time.Time) Reading {
	r := Reading{What: what, Path: path, ReadAt: at}
	if err != nil {
		r.Unknown, r.Refuses = err.Error(), true
		return r
	}
	r.TotalBytes, r.FreeBytes, r.FloorBytes = s.TotalBytes, s.FreeBytes, s.Floor()
	r.Refuses, r.Low = s.Refuses(), s.Low()
	return r
}
