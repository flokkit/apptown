package workflowexec

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema/schematest"
)

// Each runner here reads exactly the options a save lets a workflow write
// for its kind, plus what AppTown records: a field added to a runner's
// configuration without declaring it, which every save would refuse, or a
// declared option no runner reads, fails here.
func TestTheRunnersReadExactlyTheirKindsOptions(t *testing.T) {
	t.Parallel()
	for kind, decoded := range map[string]any{
		AgentKind:   agentConfig{},
		ReviewKind:  reviewConfig{},
		ScriptKind:  scriptConfig{},
		TestsKind:   testsConfig{},
		PreviewKind: previewConfig{},
		DeployKind:  deployConfig{},
		CallKind:    callConfig{},
	} {
		if err := schematest.OptionsDecodedBy(kind, decoded); err != nil {
			t.Error(err)
		}
	}
	// reconcile and scm/mr read no configuration.
	for _, kind := range []string{ReconcileKind, MergeRequestKind} {
		if err := schematest.OptionsDecodedBy(kind, struct{}{}); err != nil {
			t.Error(err)
		}
	}
}
