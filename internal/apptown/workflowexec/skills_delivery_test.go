package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// skillBlockID is a guidance block whose body carries a skill (agentCatalog).
var skillBlockID = idstest.N(10)

const skillBlockBody = "Run as non-root.\n\n<!-- skill: How to write the Dockerfile. -->\n\nRUN useradd --uid 10001 app"

// adoptSkillBlock records the project's next adoption revision with the
// skill block beside rules/plain, as an edit of its guidance would, and
// returns it.
func adoptSkillBlock(t *testing.T, db *database.DB, projectID ids.ID) *adoption.RecordedRevision {
	t.Helper()
	adoptions := adoption.New(db, adoption.Dependencies{Ways: agentCatalog{}})
	before, err := adoptions.CurrentReceipt(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := adoption.EmptyReceipt()
	receipt.Guidance = []adoption.ReceiptBlock{{Name: "rules/plain", BlockID: idstest.N(7), Version: 1}, {Name: "build/container-image", BlockID: skillBlockID, Version: 1}}
	if _, err := database.Transact(context.Background(), db, func(tx *database.Tx) (struct{}, error) {
		return struct{}{}, adoption.RecordIn(t.Context(), tx, projectID, before.Revision+1, adoption.AdoptionEdit, "skill fixture", receipt, "tester", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	current, err := adoptions.CurrentReceipt(t.Context(), projectID)
	if err != nil || current.Revision != before.Revision+1 {
		t.Fatalf("the skill block's revision: %+v %v", current, err)
	}
	return current
}

// checkSteeringNamesTheSkill: the always-read steering names the skill and
// leaves its how-to out.
func checkSteeringNamesTheSkill(t *testing.T, steering string) {
	t.Helper()
	if !strings.Contains(steering, "Run as non-root.") || !strings.Contains(steering, "Skill `build-container-image` has the how-to: How to write the Dockerfile.") {
		t.Errorf("the steering lacks the rules or the line naming the skill:\n%s", steering)
	}
	if strings.Contains(steering, "useradd") {
		t.Errorf("the steering carries the skill's how-to:\n%s", steering)
	}
}

// checkSkillFile: SKILL.md is the Agent Skills format, with the how-to.
func checkSkillFile(t *testing.T, at string, body []byte) {
	t.Helper()
	want := "---\nname: \"build-container-image\"\ndescription: \"How to write the Dockerfile.\"\n---\n\nRUN useradd --uid 10001 app\n"
	if string(body) != want {
		t.Errorf("%s:\n%s", at, body)
	}
}

// An agent step on AppTown's host finds its guidance's skills in its HOME,
// where pi and other agents (.agents/skills) and Claude Code (.claude/skills)
// discover them, and its steering carries only the rules.
func TestAHostAgentFindsItsGuidanceSkillsInItsHome(t *testing.T) {
	t.Parallel()
	var seen map[string][]byte
	a, _, stepID := admittedAgentStepOn(t, func(f *taskAppTown) (*workflow.Change, error) {
		adoptSkillBlock(t, f.db, f.projectID)
		return f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "agent work"})
	}, `{"prompt":"do it","output":{"kind":"report","name":"answer"}}`)
	pi := installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
		seen = map[string][]byte{}
		for _, rel := range []string{".agents/skills", ".claude/skills"} {
			body, err := os.ReadFile(filepath.Join(req.Workspace, ".apptown", "pi-home", rel, "build-container-image", "SKILL.md"))
			if err == nil {
				seen[rel] = body
			}
		}
		return execution.Outcome{Output: piSays("done")}
	})
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	if status, ev := stepStatus(t, a, stepID); status != workflow.StatusPass {
		t.Fatalf("step: %s %v", status, ev)
	}
	if len(seen) != 2 {
		t.Fatalf("the agent's home held the skill at %d of 2 places", len(seen))
	}
	for at, body := range seen {
		checkSkillFile(t, at, body)
	}
	steering, err := os.ReadFile(filepath.Join(pi.requests()[0].Workspace, ".apptown", "steering.md"))
	if err != nil {
		t.Fatal(err)
	}
	checkSteeringNamesTheSkill(t, string(steering))
}

// A step on a worker is sent the skills in the home the worker copies to
// its HOME, from the same pinned revision as its steering.
func TestAWorkerAgentIsSentItsGuidanceSkillsInItsHome(t *testing.T) {
	// Not parallel: the fixture sets the environment's canaries.
	f := agentOnWorkerFixture(t, nil)
	f.call.run.AdoptionID = adoptSkillBlock(t, f.exec.db, f.call.run.ProjectID).ID
	req := f.build(t)
	checkSteeringNamesTheSkill(t, req.steering.Text)
	inputs := inputsByPath(t, req.spec)
	for _, rel := range []string{".agents/skills", ".claude/skills"} {
		at := workerHomeIn + "/" + rel + "/build-container-image/SKILL.md"
		in, ok := inputs[at]
		if !ok {
			t.Errorf("no %s in the request", at)
			continue
		}
		checkSkillFile(t, at, inputContent(t, in))
	}
}
