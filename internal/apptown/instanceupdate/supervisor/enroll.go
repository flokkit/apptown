package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const containerSettlementBudget = 75 * time.Second
const managedStopBudget = containerSettlementBudget + 20*time.Second

var unitPath = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

// ManagedUnit is the exact opt-in unit enrollment accepts. There are no arbitrary
// command templates or user-supplied shell fragments. Environment is separately
// bound by content, not merely by this unit's EnvironmentFile path.
func ManagedUnit(r Registration) ([]byte, error) {
	for _, p := range []string{r.Supervisor, r.StateDir, r.EnvironmentFile} {
		if !unitPath.MatchString(p) {
			return nil, errors.New("managed unit paths contain unsupported systemd syntax")
		}
	}
	operation := "run"
	if r.Adapter == "docker" {
		operation = "run-container"
	}
	return []byte(fmt.Sprintf("[Unit]\nDescription=AppTown managed daemon\nAfter=network-online.target docker.service\nWants=network-online.target\n\n[Service]\nEnvironmentFile=%s\nExecStart=%s -state %s %s\nRestart=on-failure\nRestartSec=5\nTimeoutStopSec=%d\nKillMode=mixed\n\n[Install]\nWantedBy=multi-user.target\n", r.EnvironmentFile, r.Supervisor, r.StateDir, operation, int(managedStopBudget/time.Second))), nil
}

func validateUnit(r Registration) error {
	body, err := ManagedUnit(r)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != r.UnitSHA256 {
		return errors.New("unit does not use the registered stable startup guard")
	}
	return nil
}

// Enroll records only an already prepared, stopped managed installation. It
// does not rewrite a live unit, container restart policy, image or environment.
func Enroll(ctx context.Context, r Registration, h Host) error {
	if os.Geteuid() != 0 {
		return errors.New("enrollment requires host root administration")
	}
	if err := r.validate(); err != nil {
		return err
	}
	if _, err := registeredEnvironment(r); err != nil {
		return err
	}
	if err := validateUnit(r); err != nil {
		return err
	}
	lock, err := lockState(r.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	for _, name := range []string{"registration.json", "journal.json"} {
		if _, err := os.Lstat(filepath.Join(r.StateDir, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("installation has existing or unreadable enrollment state")
		}
	}
	obs, err := h.Observe(ctx, r)
	if err != nil {
		return err
	}
	if obs.Running {
		return errors.New("enrollment requires the prepared installation stopped")
	}
	if err := writeRecord(r.StateDir, "journal.json", journal{Format: Protocol, ConfigurationDigest: r.ConfigurationDigest}, true); err != nil {
		return err
	}
	return writeRecord(r.StateDir, "registration.json", r, true)
}
