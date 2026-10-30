package adoption

import (
	"slices"
	"strings"
	"testing"
)

// A pull keeps a workflow that a lane another of the project's packs carries
// still calls, as it keeps any called workflow.
func TestPullKeepsAWorkflowCalledByAnotherHeldPacksLane(t *testing.T) {
	t.Parallel()
	s, ways := multipackService(t)
	pid := multipackProject(t, s.db)
	caller := strings.Replace(strings.Replace(pullWorkflowYAML,
		"metadata: { name: flow }", "metadata: { name: caller }", 1),
		"uses: approval\n          with: { approvers: [bls], means: \"the direction is right\" }",
		"uses: workflow/call\n          with: { workflow: helper }", 1)
	helper := strings.Replace(pullWorkflowYAML, "metadata: { name: flow }", "metadata: { name: helper }", 1)
	for _, y := range []string{helper, caller} {
		if _, _, err := ways.PublishWorkflow(t.Context(), y, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	publishFixturePack(t, ways, "held", `{"workflows":["caller"]}`)
	publishFixturePack(t, ways, "pulled", `{"workflows":["helper"]}`)
	c, err := s.Prepare(t.Context(), pid, Request{Packs: []PackPick{{Pack: "held", Version: 1}, {Pack: "pulled", Version: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := settleServiceCandidate(s.db, s, pid, *c, OperationSelect); err != nil {
		t.Fatal(err)
	}
	publishFixturePack(t, ways, "pulled", `{"workflows":[]}`)
	p, err := ways.PackDeclaration(t.Context(), "pulled")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.PullPack(adoptionTestCaller(), pid, *p, "alice")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CurrentReceipt(t.Context(), pid)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, w := range got.Receipt.Workflows {
		names = append(names, w.Name)
	}
	if slices.Contains(result.Removed, "helper") || !slices.Contains(names, "helper") || !slices.Contains(names, "caller") {
		t.Fatalf("pull removed %v; recorded workflows %v; want caller and the helper it calls retained", result.Removed, names)
	}
}
