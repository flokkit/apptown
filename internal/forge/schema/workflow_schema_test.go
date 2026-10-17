package schema

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/yamlkeys"
)

// The served schema and the save-time parser describe the same document:
// the same keys at every level, closed where the parser is strict, and the
// same step kinds.
func TestWorkflowSchemaDescribesExactlyWhatASaveAccepts(t *testing.T) {
	t.Parallel()
	problems, err := yamlkeys.Compare(reflect.TypeOf(written{}), WorkflowJSONSchema, "workflow")
	if err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("schema and parser disagree:\n%s", strings.Join(problems, "\n"))
	}
	var s struct {
		Properties struct {
			Spec struct {
				Properties struct {
					Stages struct {
						Items struct {
							Properties struct {
								Steps struct {
									Items struct {
										Properties struct {
											Uses struct {
												Enum []string `json:"enum"`
											} `json:"uses"`
										} `json:"properties"`
									} `json:"items"`
								} `json:"steps"`
							} `json:"properties"`
						} `json:"items"`
					} `json:"stages"`
				} `json:"properties"`
			} `json:"spec"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(WorkflowJSONSchema, &s); err != nil {
		t.Fatal(err)
	}
	var supported []string
	for k := range SupportedSteps {
		supported = append(supported, k)
	}
	got := s.Properties.Spec.Properties.Stages.Items.Properties.Steps.Items.Properties.Uses.Enum
	slices.Sort(supported)
	slices.Sort(got)
	if !slices.Equal(got, supported) {
		t.Fatalf("step kinds: schema %v, parser %v", got, supported)
	}
}
