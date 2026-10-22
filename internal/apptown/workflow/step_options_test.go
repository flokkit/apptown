package workflow

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema/schematest"
)

// The approval and assertion kinds read exactly the options a save lets a
// workflow write for them.
func TestTheJudgedKindsReadExactlyTheirOptions(t *testing.T) {
	t.Parallel()
	for kind, decoded := range map[string]any{
		"approval":         ApprovalConfig{},
		"artifact/require": artifactRequirement{},
		"adr/require":      decisionRequirement{},
	} {
		if err := schematest.OptionsDecodedBy(kind, decoded); err != nil {
			t.Error(err)
		}
	}
}
