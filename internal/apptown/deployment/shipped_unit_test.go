package deployment

import (
	"os"
	"strings"
	"testing"
)

// Attaching a Docker host writes the target's block into $HOME/.ssh/config
// (upsertSSHStanza), and systemd starts services without HOME, so the native
// install's shipped unit must set it.
func TestShippedUnitSetsHomeForAttachedHostSSH(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/systemd/apptownd.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Environment=HOME=") {
			return
		}
	}
	t.Fatal("deploy/systemd/apptownd.service sets no HOME; attaching a Docker host would fail with \"set HOME\"")
}
