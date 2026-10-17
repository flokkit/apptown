package docker

import (
	"strings"

	"flokkit.io/forge/internal/gitexec/gittest"
)

// gitForDockerTest builds selected trees without importing Forge's SCM owner.
func gitForDockerTest(repo string, args ...string) (string, error) {
	cmd := gittest.Command(args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
