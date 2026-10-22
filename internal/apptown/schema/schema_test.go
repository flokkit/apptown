package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The workflow spec is the grammar of a way of working (design/architecture/workflow-spec.md):
// what it accepts defines what a workflow can BE. These tables pin the
// validation rules so a refactor cannot silently loosen them.

const validWorkflow = `
apiVersion: apptown.dev/v1
kind: Workflow
metadata:
  name: build
  description: build things
spec:
  kind: lifecycle
  stages:
    - name: build
      onEnter:
        - name: build-app
          uses: agent/pi
      steps:
        - name: accept
          uses: approval
          with:
            means: I trust the agent did what it said
    - name: ship
      steps:
        - name: deploy
          uses: deploy
      gate:
        require: [deploy]
`

func TestParseWorkflowValid(t *testing.T) {
	d, err := ParseWorkflow([]byte(validWorkflow))
	if err != nil {
		t.Fatalf("valid workflow rejected: %v", err)
	}
	if d.Metadata.Name != "build" || len(d.Spec.Stages) != 2 {
		t.Fatalf("parsed wrong: %+v", d)
	}
	if got := d.Spec.StageNames(); got[0] != "build" || got[1] != "ship" {
		t.Fatalf("stage names: %v", got)
	}
	st, idx := d.Spec.Stage("ship")
	if st == nil || idx != 1 || st.Gate == nil || st.Gate.Require[0] != "deploy" {
		t.Fatalf("Stage lookup: %+v idx=%d", st, idx)
	}
	if st, idx := d.Spec.Stage("nope"); st != nil || idx != -1 {
		t.Fatalf("missing stage should be nil,-1")
	}
}

func TestParseWorkflowRejections(t *testing.T) {
	cases := []struct {
		name, mutate, wantErr string
	}{
		{"wrong kind", `kind: Workflow`, ""}, // control — no mutation matches, stays valid
		{"kind must be Workflow", "kind: Workflow\n", "kind must be"},
		{"metadata name required", "name: build\n", "metadata.name is required"},
		{"spec kind vocabulary", "kind: lifecycle\n", "lifecycle\" or \"pipeline"},
		{"workspace vocabulary", "", "workspace must be"},
		{"at least one stage", "", "at least one stage"},
		{"stage needs steps", "", "at least one step"},
		{"duplicate stage", "", "duplicate stage"},
		{"duplicate step", "", "duplicate step"},
		{"unsupported uses", "uses: deploy\n", "unsupported uses"},
		{"gate unknown step", "require: [deploy]\n", "gate requires unknown step"},
	}
	// Each case builds its own bad document explicitly — clearer than
	// string surgery for the structural ones.
	bad := map[string]string{
		"kind must be Workflow":  strings.Replace(validWorkflow, "kind: Workflow", "kind: Pipeline", 1),
		"metadata name required": strings.Replace(validWorkflow, "name: build\n", "name: \"\"\n", 1),
		"spec kind vocabulary":   strings.Replace(validWorkflow, "kind: lifecycle", "kind: freestyle", 1),
		"workspace vocabulary":   strings.Replace(validWorkflow, "kind: lifecycle", "kind: lifecycle\n  workspace: shared", 1),
		"at least one stage":     validWorkflow[:strings.Index(validWorkflow, "  stages:")] + "  stages: []\n",
		"stage needs steps": strings.Replace(validWorkflow,
			"    - name: ship\n      steps:\n        - name: deploy\n          uses: deploy\n      gate:\n        require: [deploy]",
			"    - name: ship", 1),
		"duplicate stage": strings.Replace(validWorkflow, "- name: ship", "- name: build", 1),
		"duplicate step": strings.Replace(validWorkflow,
			"- name: accept\n          uses: approval",
			"- name: build-app\n          uses: approval", 1),
		"unsupported uses": strings.Replace(validWorkflow, "uses: deploy", "uses: teleport", 1),
		"gate unknown step": strings.Replace(validWorkflow,
			"require: [deploy]", "require: [ghost]", 1),
	}
	for _, c := range cases {
		if c.wantErr == "" {
			continue
		}
		src, ok := bad[c.name]
		if !ok {
			t.Fatalf("no bad doc for case %q", c.name)
		}
		_, err := ParseWorkflow([]byte(src))
		if err == nil {
			t.Errorf("%s: accepted a document it must reject", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.wantErr)
		}
	}
}

// Quorum config is validated at save time: a panel that can never be
// filled must not wedge a run at decision time.
func TestApprovalQuorumValidation(t *testing.T) {
	doc := func(withYaml string) string {
		return strings.Replace(validWorkflow,
			"- name: accept\n          uses: approval\n          with:\n            means: I trust the agent did what it said",
			"- name: accept\n          uses: approval\n          with:\n            means: ok\n"+withYaml, 1)
	}
	cases := []struct {
		name, with, wantErr string
	}{
		{"zero", "            quorum: 0\n", "quorum must be"},
		{"negative", "            quorum: -2\n", "quorum must be"},
		{"fractional", "            quorum: 1.5\n", "quorum must be"},
		{"unsatisfiable", "            quorum: 3\n            approvers: [a, b]\n", "could never be met"},
		{"group makes it satisfiable", "            quorum: 3\n            approvers: [\"group:parents\"]\n", ""},
		{"enough named people", "            quorum: 2\n            approvers: [a, b]\n", ""},
		{"a yes in advance says what it means", "            inAdvance: I accept it before it is worked, if it changes no code\n", ""},
		{"a yes in advance with no sentence", "            inAdvance: \"  \"\n", "inAdvance must say"},
		{"a yes in advance is one person's", "            inAdvance: I accept it in advance\n            quorum: 2\n            approvers: [a, b]\n", "quorum must be 1"},
	}
	for _, c := range cases {
		_, err := ParseWorkflow([]byte(doc(c.with)))
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: valid quorum rejected: %v", c.name, err)
		}
		if c.wantErr != "" {
			if err == nil {
				t.Errorf("%s: accepted a quorum it must reject", c.name)
			} else if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: error %q does not mention %q", c.name, err, c.wantErr)
			}
		}
	}
}

// An agent step declares private Docker with docker: true or false; any
// other value is refused rather than read as none.
func TestAgentDockerDeclaration(t *testing.T) {
	doc := func(value string) string {
		return strings.Replace(validWorkflow, "uses: agent/pi\n", "uses: agent/pi\n          with:\n            docker: "+value+"\n", 1)
	}
	for value, ok := range map[string]bool{"true": true, "false": true, "yes please": false, "1": false} {
		if _, err := ParseWorkflow([]byte(doc(value))); (err == nil) != ok {
			t.Errorf("docker: %s: %v", value, err)
		}
	}
}

// The standard of approval: an approval without a stated meaning is
// rejected at save time — otherwise it's all just LGTM. Stored documents
// predating the rule stay readable (prospective law).
func TestApprovalRequiresMeans(t *testing.T) {
	bare := strings.Replace(validWorkflow,
		"          uses: approval\n          with:\n            means: I trust the agent did what it said",
		"          uses: approval", 1)
	_, err := ParseWorkflow([]byte(bare))
	if err == nil || !strings.Contains(err.Error(), "what an OK means") {
		t.Fatalf("save must demand a means sentence, got %v", err)
	}
	blank := strings.Replace(validWorkflow, "means: I trust the agent did what it said", `means: "  "`, 1)
	if _, err := ParseWorkflow([]byte(blank)); err == nil {
		t.Fatal("whitespace means accepted")
	}
	if _, err := ParseWorkflowStored([]byte(bare)); err != nil {
		t.Fatalf("stored documents must stay readable: %v", err)
	}
}

// The stock library is the house's own work: it must pass the same
// save-time law it imposes on everyone else — including the means
// requirement. A stock workflow that fails strict validation would be
// silently skipped at sync and vanish from the library.
func TestStockWorkflowsPassStrictValidation(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "stock", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no stock workflows found: %v", err)
	}
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "kind: Workflow") {
			continue
		}
		if _, err := ParseWorkflow(b); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
		checked++
	}
	if checked < 10 {
		t.Fatalf("only %d stock workflows checked — glob broke?", checked)
	}
}

// A stock agent step that works in the project's code with private Docker
// also gets the app contract's declared needs, so it can run the app's
// tests against its database the way the landing will. Without it the
// agent of a new Rails app had no Postgres and committed tests it could
// not run.
func TestStockCodeStepsGetTheContractsNeeds(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "stock", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no stock workflows found: %v", err)
	}
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "kind: Workflow") {
			continue
		}
		doc, err := ParseWorkflow(b)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		for _, st := range doc.Spec.Stages {
			for _, s := range append(append([]Step{}, st.OnEnter...), st.Steps...) {
				if s.Uses != "agent/pi" || s.With["docker"] != true {
					continue
				}
				checked++
				if s.With["needs"] != true {
					t.Errorf("%s: step %s has private Docker but not needs: true", filepath.Base(f), s.Name)
				}
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d code steps checked — parse broke?", checked)
	}
}

// A save is held to the 1.0 format: a misspelt key, another format
// version, or the old ${{workItem.*}} name is refused, naming it; x- keys
// may hold anchors. A stored definition, accepted under the rules of its
// day, still reads.
//
// Kills: yaml.Unmarshal without known fields, which let a misspelt key
// (gates: for gate:) save as a workflow silently missing it.
func TestASaveIsHeldToTheFormatAndHistoryStillReads(t *testing.T) {
	t.Parallel()
	refused := map[string]string{
		"misspelt step key": strings.Replace(validWorkflow, "uses: agent/pi", "uses: agent/pi\n          withh: {}", 1),
		"unknown top key":   "extra: 1\n" + validWorkflow,
		"other version":     strings.Replace(validWorkflow, "apptown.dev/v1", "forge.flokkit.io/v2", 1),
		"old change alias":  strings.Replace(validWorkflow, "name: build-app", "name: build-app\n          with: {prompt: \"${{ workItem.title }}\"}", 1),
	}
	for name, src := range refused {
		if _, err := ParseWorkflow([]byte(src)); err == nil {
			t.Errorf("%s: saved", name)
		}
		if _, err := ParseWorkflowStored([]byte(src)); err != nil && name != "other version" {
			t.Errorf("%s: a stored definition no longer reads: %v", name, err)
		}
	}
	for name, src := range map[string]string{
		"no apiVersion":   strings.Replace(validWorkflow, "apiVersion: apptown.dev/v1\n", "", 1),
		"an x- anchor":    "x-shared: &shared {prompt: hi}\n" + validWorkflow,
		"the change name": strings.Replace(validWorkflow, "name: build-app", "name: build-app\n          with: {prompt: \"${{ change.title }}\"}", 1),
	} {
		if _, err := ParseWorkflow([]byte(src)); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}
