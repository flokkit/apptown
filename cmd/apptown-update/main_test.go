package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnknownSupervisorCommandsCannotCreateOrControlAnInstallation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	for _, args := range [][]string{{"-state", path, "exec", "sh"}, {"-state", path, "install", "release"}, {"-state", path, "run", "other-service"}, {"-state", path, "set-phase", "running"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err == nil {
			t.Fatal("accepted arbitrary supervisor operation", args)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("refused command created control state", err)
		}
	}
}
