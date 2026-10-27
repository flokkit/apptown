//go:build !linux && !darwin

package supervisor

import (
	"errors"
	"os"
)

func secureFile(string) error      { return errors.New("managed supervision requires Linux") }
func secureDirectory(string) error { return errors.New("managed supervision requires Linux") }
func lockState(string) (*os.File, error) {
	return nil, errors.New("managed supervision requires Linux")
}

func lockNamed(string, string) (*os.File, error) {
	return nil, errors.New("managed supervision requires Linux")
}

func secureArtifact(string) error { return errors.New("managed supervision requires Linux") }
