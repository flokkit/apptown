package supervisor

import (
	"errors"
	"os"
	"path/filepath"
)

// startupFailure retains an uncertain registered process outcome before the
// launch lease is released. It is independent of update requests: a failed
// host observation cannot fabricate a product actor or recovery transaction.
type startupFailure struct {
	Format              int    `json:"format"`
	ConfigurationDigest string `json:"configurationDigest"`
	Reason              string `json:"reason"`
}

func (s *Store) startupFailure() error {
	var failure startupFailure
	err := readRecord(filepath.Join(s.registration.StateDir, "startup-failure.json"), &failure)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if failure.Format != Protocol || failure.ConfigurationDigest != s.registration.ConfigurationDigest || failure.Reason == "" {
		return errors.New("invalid retained startup failure")
	}
	return errors.New(failure.Reason)
}
