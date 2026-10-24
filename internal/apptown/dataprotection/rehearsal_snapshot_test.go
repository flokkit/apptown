package dataprotection

import (
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/stock"
)

// One rehearsal must keep the same images for staging, booting and probing,
// even when a newer list is installed after staging the backup.
func TestARehearsalKeepsItsToolVersionsThroughItsBoot(t *testing.T) {
	t.Parallel()
	w := newSharedRehearsalWorld(t)
	s := w.s.Service
	first := stock.Versions().Current()
	first.Images.Sh = "first-helper"
	tools := toolversions.New(first)
	s.env = deployment.NewEnvironment(s.targets, deployment.EnvironmentDependencies{
		Secrets: s.secrets, Integrations: s.integrations, Needs: s.catalog,
		Projects: s.projects, Probe: &docker.Probe{Settle: time.Millisecond},
		Routes: s.env.Routes(), ToolVersions: tools,
	})
	second := first
	second.Images.Sh = "second-helper"
	second.Images.DF = "second-df"
	w.host.afterStage = func() { tools.Install(second) }
	op := w.rehearse(t)
	if !endedAs(op, OperationVerified) {
		t.Fatalf("rehearsal %s: %s", op.State, op.Detail)
	}
	for _, args := range w.host.Commands() {
		if slices.Contains(args, "second-helper") || slices.Contains(args, "second-df") {
			t.Fatalf("rehearsal re-read tool versions after staging: %v", args)
		}
	}
}
