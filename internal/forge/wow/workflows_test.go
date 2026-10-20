package wow

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/stock"
)

var sprintf = fmt.Sprintf

const laneYAML = `apiVersion: apptown.dev/v1
kind: Workflow
metadata:
  name: %s
  description: %s
%sspec:
  kind: pipeline
  role: landing
  stages:
    - name: gates
      steps:
        - {name: approve, uses: approval, with: {approvers: [x], means: "I read it"}}
`

func TestWorkflowLibraryVersionsAndStockProtection(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	wf := NewWorkflows(db)
	if _, _, err := wf.Put(t.Context(), sprintf(laneYAML, "", "nameless", ""), "alice"); err == nil {
		t.Fatal("a nameless workflow was accepted")
	}
	src := func(name, desc, extra string) string { return sprintf(laneYAML, name, desc, extra) }
	name, v, err := wf.Put(t.Context(), src("ours/tidy", "first", ""), "alice")
	if err != nil || name != "ours/tidy" || v != 1 {
		t.Fatalf("create: %s %d %v", name, v, err)
	}
	if _, v, err = wf.Put(t.Context(), src("ours/tidy", "second", ""), "alice"); err != nil || v != 2 {
		t.Fatalf("bump: %d %v", v, err)
	}
	if _, y, err := wf.Version(t.Context(), "ours/tidy", 0); err != nil || y != src("ours/tidy", "second", "") {
		t.Fatalf("latest: %v", err)
	}
	if history, _ := wf.History(t.Context(), "ours/tidy"); len(history) != 2 || history[0].Version != 2 {
		t.Fatalf("history newest first: %+v", history)
	}
	if receipts := wowReceipts(t, db, "library.workflow.", "ours/tidy"); len(receipts) != 2 {
		t.Fatalf("workflow receipts: %+v", receipts)
	}

	if err := wf.StockInsert(t.Context(), "land", "stock", src("land", "stock", "  system: true\n")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wf.Put(t.Context(), src("land", "mine", ""), "alice"); !errors.Is(err, ErrStockWorkflow) {
		t.Fatalf("a stock name was re-authored: %v", err)
	}
	if err := wf.Delete(t.Context(), "land", "alice"); !errors.Is(err, ErrStockWorkflow) {
		t.Fatalf("a stock workflow was deleted: %v", err)
	}
	if err := wf.Delete(t.Context(), "nobody", "alice"); !errors.Is(err, ErrNoWorkflow) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absence: %v", err)
	}
	if _, err := wf.History(t.Context(), "nobody"); !errors.Is(err, ErrNoWorkflow) || fault.KindOf(err) != fault.NotFound {
		t.Fatalf("history of an unknown workflow: %v", err)
	}
	if names, _ := wf.CustomNames(t.Context()); len(names) != 1 || names[0] != "ours/tidy" {
		t.Fatalf("custom names: %v", names)
	}
	if !wf.IsSystem(t.Context(), "land") || wf.IsSystem(t.Context(), "ours/tidy") {
		t.Fatal("the library's word on system workflows")
	}
}

// Whether a workflow's text declares it a system workflow is the parse's
// answer, asked once or many times, for a system text, an ordinary one and
// one that does not parse (which is not a system workflow).
func TestASystemDeclarationIsTheTextsAnswerEveryTime(t *testing.T) {
	t.Parallel()
	land, err := fs.ReadFile(stock.FS, "land.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tidy := sprintf(laneYAML, "ours/tidy", "tidy", "")
	declared := sprintf(laneYAML, "ours/standin", "stand-in", "  system: true\n")
	for range 2 {
		if !declaresSystem(string(land)) || !declaresSystem(declared) || declaresSystem(tidy) || declaresSystem("metadata: [unclosed") {
			t.Fatal("a system declaration differs from the text's")
		}
	}
}
