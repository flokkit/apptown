package workflowexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func TestFreshWorkflowAgentReceivesDeclaredContextFromStartingCommit(t *testing.T) {
	t.Parallel()
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "present"}[present], func(t *testing.T) {
			t.Parallel()
			f, _ := newTaskAppTown(t, "build", schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "implement", Uses: AgentKind, With: map[string]any{"prompt": "Do the work."}}}}}})
			ways := wow.New(f.db, wow.Dependencies{})
			b, err := ways.CreateBlock(t.Context(), "rules/context", "<!-- context: [{\"path\":\"PROJECT-NOTES.md\",\"maxBytes\":6144}] -->\nKeep it plain.", "", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			receipt := adoption.EmptyReceipt()
			receipt.Guidance = []adoption.ReceiptBlock{{Name: b.Name, BlockID: b.ID, Version: 1}}
			if err := f.db.InTx(t.Context(), func(tx *database.Tx) error {
				return adoption.RecordIn(t.Context(), tx, f.projectID, 2, adoption.AdoptionCreate, "context fixture", receipt, "fixture", time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			const fact = "Distinctive accepted project fact."
			if present {
				hubCommit(t, f.hub, map[string]string{"PROJECT-NOTES.md": "# Project notes\n- " + fact + " (agent · change 1)\n"})
			}
			base := gittest.Run(t, f.hub, "rev-parse", "HEAD")
			run := f.startChangeRun(t, "build", f.ctx)
			step := pendingStepNamed(t, f, run.ID, "implement")
			f.agents.adoption = adoption.New(f.db, adoption.Dependencies{Ways: ways})
			// Main moves after admission; it must not prime this run.
			hubCommit(t, f.hub, map[string]string{"PROJECT-NOTES.md": "unaccepted later main fact"})
			calls := 0
			installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
				calls++
				raw, err := os.ReadFile(filepath.Join(req.Workspace, ".apptown", "steering.md"))
				if err != nil {
					t.Fatal(err)
				}
				prompt := string(raw)
				if strings.Contains(prompt, fact) != present || strings.Contains(prompt, "unaccepted") {
					t.Fatalf("wrong starting context: %s", prompt)
				}
				if present && !strings.Contains(prompt, "commit "+base) {
					t.Fatal("context commit missing")
				}
				return execution.Outcome{Output: piSays("done")}
			})
			if err := f.work.ExecuteStep(f.ctx, step); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("fresh initial invocations: %d", calls)
			}
		})
	}
}
