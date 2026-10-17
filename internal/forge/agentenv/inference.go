package agentenv

import (
	"fmt"

	"flokkit.io/forge/internal/execution"
)

// RequireInferenceRuntime keeps untrusted model agents away from daemon secrets.
func RequireInferenceRuntime(enabled bool, executor string) error {
	if executor != execution.Docker {
		return fmt.Errorf("model agents require the Docker executor: local processes can read the daemon's provider credentials")
	}
	if !enabled {
		return fmt.Errorf("inference is disabled: configure the private inference listener, runtime URL and allowed worker networks")
	}
	return nil
}
