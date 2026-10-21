package schema

import (
	"encoding/json"
	"maps"
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

// The served schema closes each step kind's with to that kind's options,
// for steps and for onEnter, so an editor flags minApprovals as the save
// would.
func TestWorkflowSchemaGivesEachStepKindItsOptions(t *testing.T) {
	t.Parallel()
	var s struct {
		Properties struct {
			Spec struct {
				Properties struct {
					Stages struct {
						Items struct {
							Properties map[string]struct {
								Items struct {
									AllOf []struct {
										If struct {
											Properties struct {
												Uses struct {
													Const string `json:"const"`
												} `json:"uses"`
											} `json:"properties"`
										} `json:"if"`
										Then struct {
											Properties struct {
												With struct {
													AdditionalProperties *bool          `json:"additionalProperties"`
													Properties           map[string]any `json:"properties"`
												} `json:"with"`
											} `json:"properties"`
										} `json:"then"`
									} `json:"allOf"`
								} `json:"items"`
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
	for _, list := range []string{"steps", "onEnter"} {
		described := map[string][]string{}
		for _, c := range s.Properties.Spec.Properties.Stages.Items.Properties[list].Items.AllOf {
			with := c.Then.Properties.With
			if with.AdditionalProperties == nil || *with.AdditionalProperties {
				t.Errorf("%s %s: with is not closed", list, c.If.Properties.Uses.Const)
			}
			keys := slices.Sorted(maps.Keys(with.Properties))
			described[c.If.Properties.Uses.Const] = keys
		}
		for kind := range SupportedSteps {
			want, _ := StepOptions(kind)
			slices.Sort(want)
			got, ok := described[kind]
			if !ok || !slices.Equal(got, want) {
				t.Errorf("%s %s: schema describes %v, a save takes %v", list, kind, got, want)
			}
		}
		if len(described) != len(SupportedSteps) {
			t.Errorf("%s: schema describes %d kinds, AppTown runs %d", list, len(described), len(SupportedSteps))
		}
	}
}
