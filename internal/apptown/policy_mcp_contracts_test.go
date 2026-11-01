package apptown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/mcpapi"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func checkMCPReadPolicies(t *testing.T) {
	t.Parallel()
	a, boss := mcpWriteFixture(t)
	revokeConformanceDefaultRead(t, a)
	outsider := mcp_token(t, a, "outsider", "outsider", "member")
	p, _ := a.Projects.BySlug(context.Background(), "app")
	// A real committed contract and guidance are protected data, not an empty
	// response that would pass equally with a broken permission check.
	repo := a.projectRepoPath("app")
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".apptown/app.yaml"), []byte("# private-contract\nparts: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "contract")
	adopt(t, a, p.ID, "private-guidance", 1, "[]")
	public, _, _ := fleetRepo(t, a, "public-project", map[string]string{"README.md": "public"})
	a.addGrant("group", identity.GroupAllUsers, access.RoleProjectMaintainer, "", access.ScopeProject, fmt.Sprint(public), "boss", "")
	a.addGrant("group", identity.GroupAllUsers, access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(public), "boss", "")
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "policy", Version: 1}, Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "approve", Uses: "approval"}}}}}})
	var privateChange, privateRun ids.ID
	for _, pid := range []ids.ID{p.ID, public} {
		pr := a.projectByID(context.Background(), pid)
		if _, err := a.recordAdoption(pr, "edit", "policy", "boss"); err != nil {
			t.Fatal(err)
		}
		ch, err := a.Work.Create(asPerson("boss"), workflow.ChangeDraft{ProjectID: pid, Title: "policy-work-" + pr.Slug})
		if err != nil {
			t.Fatal(err)
		}
		run, err := a.Work.StartRun(asPerson("boss"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "policy"})
		if err != nil {
			t.Fatal(err)
		}
		if pid == p.ID {
			privateChange, privateRun = ch.ID, run.ID
		}
	}
	a.Work.Wait()
	for _, tc := range []struct {
		tool string
		args map[string]any
		key  string
	}{
		{"get_project", map[string]any{"slug": "app"}, "slug"},
		{"get_project_guidance", map[string]any{"slug": "app"}, "text"},
		{"get_application_contract", map[string]any{"slug": "app"}, "text"},
		{"list_changes", map[string]any{"slug": "app"}, "changes"},
		{"get_change", map[string]any{"id": privateChange}, "title"},
		{"get_change_diff", map[string]any{"id": privateChange}, "diff"},
		{"get_change_evidence", map[string]any{"id": privateChange}, "steps"},
		{"get_run", map[string]any{"id": privateRun}, "steps"},
	} {
		denied := mcp_call(t, a, outsider, tc.tool, tc.args)
		if denied["isError"] != true || denied["structuredContent"] != nil {
			t.Fatalf("%s exposed private record: %+v", tc.tool, denied)
		}
		allowed := mcp_call(t, a, boss, tc.tool, tc.args)
		out, _ := allowed["structuredContent"].(map[string]any)
		if allowed["isError"] == true || out[tc.key] == nil {
			t.Fatalf("%s missing permitted data: %+v", tc.tool, allowed)
		}
	}
	for _, tool := range []string{"list_projects", "get_waiting_on_you"} {
		for _, tc := range []struct {
			token   string
			private bool
		}{{outsider, false}, {boss, true}} {
			res := mcp_call(t, a, tc.token, tool, map[string]any{})
			body := mustJSON(res["structuredContent"])
			if res["isError"] == true || strings.Contains(body, `"app"`) != tc.private || !strings.Contains(body, `"public-project"`) {
				t.Fatalf("%s filtered data: %+v", tool, res)
			}
		}
	}
}

func checkMCPWritePolicies(t *testing.T) {
	t.Parallel()
	a, boss := mcpWriteFixture(t)
	viewer := mcp_token(t, a, "viewer", "viewer", "member")
	p, _ := a.Projects.BySlug(context.Background(), "app")
	if _, err := a.recordAdoption(&p, "edit", "policy", "boss"); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a,
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "policy", Version: 1}, Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "agent", Uses: "agent/pi"}, {Name: "approve", Uses: "approval"}}}}}},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "lifecycle", Role: "landing", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "approve", Uses: "approval"}}}}}},
	)
	if err := a.lifecycle.SelectLane(t.Context(), p.ID, "policy", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi"}
		deps.Runners = map[string]workflow.Runner{"agent/pi": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
	call := func(token, tool string, args map[string]any) map[string]any {
		t.Helper()
		res := mcp_call(t, a, token, tool, args)
		if res["isError"] == true {
			t.Fatalf("%s: %+v", tool, res)
		}
		out, ok := res["structuredContent"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no result: %+v", tool, res)
		}
		return out
	}
	deny := func(tool string, args map[string]any) {
		t.Helper()
		if res := mcp_call(t, a, viewer, tool, args); res["isError"] != true || res["structuredContent"] != nil {
			t.Fatalf("%s was not refused: %+v", tool, res)
		}
	}
	deny("create_change", map[string]any{"slug": "app", "title": "policy work"})
	rows, err := a.Work.Changes(asPerson("boss"), p.ID, workflow.ChangeList{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("denial created change: %+v %v", rows, err)
	}
	out := call(boss, "create_change", map[string]any{"slug": "app", "title": "policy work"})
	id := idstest.Parse(t, out["id"].(string))
	deny("start_change_lane", map[string]any{"id": id, "lane": "policy"})
	rowsBefore, err := a.Work.ChangeRuns(asPerson("boss"), id, workflow.ChangeRuns{})
	if err != nil || len(rowsBefore) != 0 {
		t.Fatalf("denial admitted run: %+v %v", rowsBefore, err)
	}
	call(boss, "start_change_lane", map[string]any{"id": id, "lane": "policy"})
	a.Work.Wait()
	ch, err := a.Work.Change(asPerson("boss"), id)
	if err != nil || ch.RunID.IsZero() {
		t.Fatalf("run missing: %+v %v", ch, err)
	}
	for _, tool := range []string{"send_change_back", "request_change_landing", "abandon_change"} {
		before, err := a.Work.Snapshot(asPerson("boss"), ch.RunID, workflow.DetailView)
		if err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"id": id}
		if tool == "send_change_back" {
			args["note"] = "try again"
		}
		if tool == "abandon_change" {
			args["reason"] = "stop"
		}
		deny(tool, args)
		after, err := a.Work.Snapshot(asPerson("boss"), ch.RunID, workflow.DetailView)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("denied %s changed work: %v", tool, err)
		}
	}
	// Exercise the actual checks adapter with a counted downstream effect. This
	// isolates the permission boundary from execution of arbitrary build tools.
	checks := &policyChecks{}
	tools := mcpapi.NewChecks(mcpapi.ChecksDependencies{Scope: mcpapi.NewScope(a.Projects, a.Access), Work: a.Work, Verification: checks})
	for _, who := range []string{"viewer", "boss"} {
		role := "member"
		if who == "boss" {
			role = "admin"
		}
		c := &mcpapi.Call{Context: callerMCPContext(context.Background(), &authIdentity{User: who, Role: role, Via: "token"}, who+" via mcp/test")}
		got, err := tools[0].Run(c, map[string]any{"id": id.String()})
		if who == "viewer" {
			if err == nil || got != nil || checks.calls != 0 {
				t.Fatal("denied checks reached executor")
			}
		} else if err != nil || checks.calls != 1 || got == nil {
			t.Fatalf("authorized checks: %+v %v", got, err)
		}
	}
	sent := call(boss, "send_change_back", map[string]any{"id": id, "note": "try again"})
	a.Work.Wait()
	resumed, err := a.Work.Snapshot(asPerson("boss"), idstest.Parse(t, sent["runId"].(string)), workflow.DetailView)
	if err != nil || len(resumed.Steps) != 2 || resumed.Steps[0].Attempt < 2 {
		t.Fatalf("send-back missing retry: %+v %v", resumed, err)
	}
	if _, err := a.Work.Approve(asPerson("boss"), workflow.Approval{StepID: resumed.Steps[1].ID, Decision: workflow.VoteApprove, Adds: shownAdds(t, a, asPerson("boss"), resumed.Steps[1].ID)}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	call(boss, "request_change_landing", map[string]any{"id": id})
	a.Work.Wait()
	changed, err := a.Work.Change(asPerson("boss"), id)
	if err != nil || changed.State != workflow.ChangeLanding {
		t.Fatalf("landing missing: %+v %v", changed, err)
	}
	call(boss, "abandon_change", map[string]any{"id": id, "reason": "done testing"})
	a.Work.Wait()
	changed, err = a.Work.Change(asPerson("boss"), id)
	if err != nil || changed.State != workflow.ChangeAbandoned {
		t.Fatalf("abandonment missing: %+v %v", changed, err)
	}
}

type policyChecks struct{ calls int }

func (p *policyChecks) Check(context.Context, *workflow.Change, verification.Selection) (map[string]any, error) {
	p.calls++
	return map[string]any{"checked": true}, nil
}
