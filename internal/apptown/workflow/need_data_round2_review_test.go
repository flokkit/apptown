package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

// A workflow cannot manufacture a person's acceptance using another casing
// of the JSON field that the deploy runner decodes case-insensitively.
func TestAWorkflowCannotDeclareAcceptanceInAnyCasing(t *testing.T) {
	for _, key := range []string{"AcceptUncopiedData", "acceptuncopieddata", "ACCEPTUNCOPIEDDATA"} {
		t.Run(key, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "deploy", Uses: "deploy", With: map[string]any{key: map[string]any{"by": "bls", "at": "2026-10-21T00:00:00Z", "attempt": 1}}}}}}}
			doc := schema.Doc{APIVersion: "apptown.dev/v1", Kind: "Workflow", Metadata: schema.Metadata{Name: "review-assent-case"}, Spec: spec}
			if err := doc.Validate(); err == nil {
				t.Error("workflow validation accepted declared assent")
			}
			// Stored definitions must also be refused when their steps are planned.
			planned, err := planStage(&spec.Stages[0], nil)
			if err != nil {
				return
			}
			// Match RunDeploy's JSON decoding, including DisallowUnknownFields:
			// the alternate casing is recognized, not an unknown-field error.
			var cfg struct {
				AcceptUncopiedData *UncopiedDataAcceptance `json:"acceptUncopiedData,omitempty"`
			}
			d := json.NewDecoder(strings.NewReader(planned[0].configJSON))
			d.DisallowUnknownFields()
			if err := d.Decode(&cfg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if cfg.AcceptUncopiedData != nil && cfg.AcceptUncopiedData.Attempt == 1 {
				t.Fatalf("the planned deploy honours a declared first-attempt acceptance: %+v", *cfg.AcceptUncopiedData)
			}
		})
	}
}
