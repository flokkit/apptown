package agentenv

import (
	"strings"
	"testing"
)

// dind.Builder creates and selects "apptown". Agents must be told the name
// they can actually use when inspecting or selecting the supplied builder.
func TestDockerSteeringNamesTheSuppliedBuilder(t *testing.T) {
	if !strings.Contains(DockerInstructions, "a buildx builder named `apptown`") {
		t.Fatal("Docker steering does not name the supplied apptown builder")
	}
}
