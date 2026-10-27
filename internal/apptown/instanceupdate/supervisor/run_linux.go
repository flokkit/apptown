//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// RunDaemon is the stable systemd ExecStart guard. It owns the daemon child and
// its launch lease through settlement. Ordinary task Runner deliberately cannot
// supply this service lifetime/environment contract; bounded observations use it.
func RunDaemon(ctx context.Context, dir string, h Host) error {
	if os.Geteuid() != 0 {
		return errors.New("managed startup requires host root administration")
	}
	s, err := startupStore(dir)
	if err != nil {
		return err
	}
	defer s.Close()
	r := s.registration
	if r.Adapter != "systemd" || r.DaemonUID != 0 {
		return errors.New("systemd guard requires the registered root daemon")
	}
	if _, err := h.unit(ctx, r); err != nil {
		return err
	}
	if err := effectiveEnvironment(r, os.Environ()); err != nil {
		return err
	}
	executable := filepath.Join(r.ReleaseDir, "bin", "apptownd")
	if err := compareFile(executable, r.ExecutableSHA256); err != nil {
		return err
	}
	cmd := exec.Command(executable)
	cmd.Dir = r.ReleaseDir
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	// Pdeathsig is tied to the creating thread, so retain that thread until
	// the daemon settles rather than letting the Go runtime retire it early.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	signalErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	if errors.Is(signalErr, syscall.ESRCH) {
		signalErr = nil
	}
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		return errors.Join(ctx.Err(), signalErr, err)
	case <-timer.C:
	}
	killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(killErr, syscall.ESRCH) {
		killErr = nil
	}
	return errors.Join(ctx.Err(), signalErr, killErr, <-done)
}

// RunContainer owns only the exact registered container. The container has no
// autonomous restart policy; reboot re-enters this durable guard first.
func RunContainer(ctx context.Context, dir string, h Host) (result error) {
	if os.Geteuid() != 0 {
		return errors.New("managed startup requires host root administration")
	}
	s, err := startupStore(dir)
	if err != nil {
		return err
	}
	defer s.Close()
	r := s.registration
	if r.Adapter != "docker" {
		return errors.New("registered installation is not a container")
	}
	obs, err := h.Observe(ctx, r)
	if err != nil {
		return err
	}
	if obs.Running {
		return errors.New("registered container was started outside its stable controller")
	}
	// Start errors have an unknown effect. Once attempted, this controller always
	// observes/stops the exact registered container before releasing its lease.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerSettlementBudget)
		defer cancel()
		observed, observeErr := h.Observe(cleanup, r)
		if observeErr == nil && observed.Running {
			_, stopErr := h.runFor(cleanup, 65*time.Second, "docker", "--host", "unix://"+r.DockerSocket, "stop", "--time", "60", r.Container)
			observed, observeErr = h.Observe(cleanup, r)
			result = errors.Join(result, stopErr)
		}
		if observeErr != nil || observed.Running {
			result = errors.Join(result, observeErr, errors.New("registered container settlement is unconfirmed"), s.retainStartupFailure())
		}
	}()
	if _, err := h.run(ctx, "docker", "--host", "unix://"+r.DockerSocket, "start", r.Container); err != nil {
		return err
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			now, err := h.Observe(ctx, r)
			if err != nil {
				return err
			}
			if !now.Running {
				return errors.New("registered daemon container exited")
			}
		}
	}
}

// startupStore holds a separate launch lease until the registered child settles.
// A recovery coordinator must obtain this lease after stopping the service before
// touching its database/files. The durable journal is still authoritative: a new
// guard always reads it, including when no supervisor process survived a reboot.
func startupStore(dir string) (*Store, error) {
	lock, err := lockNamed(dir, "startup.lock")
	if err != nil {
		return nil, err
	}
	var r Registration
	if err := readRecord(filepath.Join(dir, "registration.json"), &r); err != nil {
		_ = lock.Close()
		return nil, err
	}
	if err := r.validate(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	if r.StateDir != dir {
		_ = lock.Close()
		return nil, errors.New("startup registration differs")
	}
	s := &Store{registration: r, lock: lock}
	if err := validateUnit(r); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.OrdinaryStart(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) retainStartupFailure() error {
	return writeRecord(s.registration.StateDir, "startup-failure.json", startupFailure{Format: Protocol, ConfigurationDigest: s.registration.ConfigurationDigest, Reason: "Registered container startup or shutdown could not be confirmed; host recovery is required before another start."}, false)
}
