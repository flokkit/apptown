package adoption

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

func TestSystemBindingsAndLanes(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	wf := wow.NewWorkflows(db)
	src := func(name, extra string) string { return fmt.Sprintf(laneYAML, name, "d", extra) }
	if _, _, err := wf.Put(t.Context(), src("ours/land", "  system: true\n"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wf.Put(t.Context(), src("ours/plain", ""), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := wf.BindSystem(t.Context(), "deploy", "ours/land", "alice"); !errors.Is(err, wow.ErrNotSystemWorkflow) {
		t.Fatalf("an unknown stock name was bound: %v", err)
	}
	if err := wf.BindSystem(t.Context(), "land", "ours/plain", "alice"); fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("a workflow without system: true stood in for land: %v", err)
	}
	if err := wf.BindSystem(t.Context(), "land", "ours/missing", "alice"); !errors.Is(err, wow.ErrNoWorkflow) || fault.KindOf(err) != fault.NotFound {
		t.Fatalf("a missing stand-in: %v", err)
	}
	if err := wf.BindSystem(t.Context(), "land", "ours/land", "alice"); err != nil || wf.SystemFor(t.Context(), "land") != "ours/land" {
		t.Fatalf("bind: %v %s", err, wf.SystemFor(t.Context(), "land"))
	}
	if err := wf.ResetSystem(t.Context(), "land", "alice"); err != nil || wf.SystemFor(t.Context(), "land") != "land" {
		t.Fatalf("reset: %v", err)
	}
	if r := ledger(t, db, ids.Nil, "system-workflow."); len(r) != 2 {
		t.Fatalf("system workflow receipts: %+v", r)
	}

	lanes := newLanes(db)
	pid := adoptionTestProject(t, db, "ln", "Ln")
	selected := func() bool {
		t.Helper()
		on, err := lanes.Selected(t.Context(), pid, "ours/plain")
		if err != nil {
			t.Fatal(err)
		}
		return on
	}
	set := func(on bool) error {
		return db.InTx(context.Background(), func(tx *database.Tx) error { return setLaneIn(t.Context(), tx, pid, "ours/plain", on, time.Now()) })
	}
	if selected() {
		t.Fatal("no row means not selected")
	}
	if err := set(true); err != nil || !selected() {
		t.Fatalf("select: %v", err)
	}
	if err := set(false); err != nil || selected() {
		t.Fatalf("deselect: %v", err)
	}
}

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
