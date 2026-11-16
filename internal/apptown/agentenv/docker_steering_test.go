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
	if !strings.Contains(DockerInstructions, "`--network host --dns 127.0.0.11`") || !strings.Contains(DockerInstructions, "`dind:PORT`") {
		t.Fatal("Docker steering does not explain how rehearsal containers reach declared needs")
	}
}
