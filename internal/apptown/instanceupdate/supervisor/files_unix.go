//go:build linux || darwin

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func secureFile(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("control file %s has unsafe ownership or permissions", path)
	}
	return nil
}
func secureDirectory(path string) error {
	if !canonical(path) {
		return errors.New("control directory must be canonical")
	}
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("control directory has a non-directory or symlink ancestor")
		}
		if p == path {
			if owner.Uid != uint32(os.Geteuid()) || st.Mode().Perm()&0077 != 0 {
				return errors.New("control directory needs private ownership and mode0700")
			}
		} else if owner.Uid != 0 && owner.Uid != uint32(os.Geteuid()) {
			return errors.New("control directory has an untrusted owner ancestor")
		} else if st.Mode().Perm()&0022 != 0 && st.Mode()&os.ModeSticky == 0 {
			return errors.New("control directory has a writable ancestor")
		}
		if p == "/" {
			break
		}
	}
	return nil
}
func lockState(dir string) (*os.File, error) { return lockNamed(dir, "owner.lock") }
func lockNamed(dir, name string) (*os.File, error) {
	if err := secureDirectory(dir); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(dir, name), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "supervisor ownership")
	if err := secureFile(filepath.Join(dir, name)); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("installation already has a supervisor: %w", err)
	}
	return f, nil
}

func secureArtifact(path string) error {
	if !canonical(path) {
		return errors.New("artifact path must be canonical")
	}
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || (owner.Uid != 0 && owner.Uid != uint32(os.Geteuid())) || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact has untrusted ownership or a symlink")
		}
		if p == path {
			if !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
				return errors.New("registered artifact is not an immutable regular file")
			}
		} else if !st.IsDir() || (st.Mode().Perm()&0022 != 0 && st.Mode()&os.ModeSticky == 0) {
			return errors.New("artifact has a writable or invalid ancestor")
		}
		if p == "/" {
			break
		}
	}
	return nil
}
