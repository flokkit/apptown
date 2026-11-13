package schema

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/yamlkeys"
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
	for k, typ := range StepTypes {
		if !typ.Authored {
			continue
		}
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
		authored := 0
		for kind, typ := range StepTypes {
			if !typ.Authored {
				continue
			}
			authored++
			want, _ := StepOptions(kind)
			slices.Sort(want)
			got, ok := described[kind]
			if !ok || !slices.Equal(got, want) {
				t.Errorf("%s %s: schema describes %v, a save takes %v", list, kind, got, want)
			}
		}
		if len(described) != authored {
			t.Errorf("%s: schema describes %d kinds, AppTown runs %d", list, len(described), authored)
		}
	}
}

// Editors and save-time validation consume the same kind registration.
func TestFixKindsComeFromRegistration(t *testing.T) {
	t.Parallel()
	var document any
	if err := json.Unmarshal(WorkflowJSONSchema, &document); err != nil {
		t.Fatal(err)
	}
	found := 0
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if properties, ok := v["properties"].(map[string]any); ok {
				if _, targeted := properties["for"]; targeted {
					if kind, ok := properties["kind"].(map[string]any); ok {
						raw, _ := json.Marshal(kind["enum"])
						var names []string
						if err := json.Unmarshal(raw, &names); err != nil {
							t.Fatal(err)
						}
						if !slices.Equal(names, fix.Names()) {
							t.Fatalf("editor kinds %v, registered %v", names, fix.Names())
						}
						found++
					}
				}
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(document)
	if found != 2 {
		t.Fatalf("found %d fix declarations, want onEnter and steps", found)
	}
	for _, kind := range append(fix.Names(), "unregistered") {
		spec := Spec{Kind: "pipeline", Role: "working", Stages: []Stage{{Name: "work", Steps: []Step{{Name: "fix", Uses: "fix", With: map[string]any{"kind": kind}}}}}}
		err := (&Doc{APIVersion: "apptown/v1", Kind: "Workflow", Metadata: Metadata{Name: "test"}, Spec: spec}).Validate()
		_, registered := fix.Lookup(kind)
		if (err == nil) != registered {
			t.Fatalf("kind %q: registered=%v error=%v", kind, registered, err)
		}
	}
}

func TestAuthoredStepRegistrationReachesParserAndServedSchema(t *testing.T) {
	StepTypes["synthetic/check"] = stepType{Authored: true, Options: []string{"finding"}, Check: true, CheckExecutor: true}
	defer delete(StepTypes, "synthetic/check")
	src := []byte("kind: Workflow\nmetadata:\n  name: synthetic\nspec:\n  kind: pipeline\n  stages:\n    - name: check\n      steps:\n        - name: check\n          uses: synthetic/check\n          with:\n            finding: yes\n")
	if _, err := ParseWorkflow(src); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(workflowJSONSchema(), &doc); err != nil {
		t.Fatal(err)
	}
	stage := doc["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)["stages"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for _, list := range []string{"steps", "onEnter"} {
		item := stage[list].(map[string]any)["items"].(map[string]any)
		kinds := item["properties"].(map[string]any)["uses"].(map[string]any)["enum"].([]any)
		if !slices.Contains(kinds, any("synthetic/check")) || slices.Contains(kinds, any("scm/merged")) {
			t.Fatalf("%s kinds: %v", list, kinds)
		}
		found := false
		for _, raw := range item["allOf"].([]any) {
			c := raw.(map[string]any)
			if c["if"].(map[string]any)["properties"].(map[string]any)["uses"].(map[string]any)["const"] != "synthetic/check" {
				continue
			}
			with := c["then"].(map[string]any)["properties"].(map[string]any)["with"].(map[string]any)
			_, found = with["properties"].(map[string]any)["finding"]
		}
		if !found {
			t.Errorf("%s has no registered options", list)
		}
	}
	if _, err := ParseWorkflow([]byte(strings.ReplaceAll(string(src), "synthetic/check", "scm/merged"))); err == nil {
		t.Fatal("internal merge gate accepted in authored workflow")
	}
}
