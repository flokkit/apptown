package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

// A custom pack, published as v1 with its blocks pinned, for the tests.
func testPack(t *testing.T, a *App, name string, guidance []string) {
	t.Helper()
	spec := mustJSON(wow.PackSpec{Guidance: guidance, Workflows: []string{"quick-change"}})
	if version, err := a.Ways.PublishPack(t.Context(), name, "t", spec, ""); err != nil || version != 1 {
		t.Fatalf("publish %s: version=%d err=%v", name, version, err)
	}
}

func appendTestGuidance(t *testing.T, a *App, projectID ids.ID, name string, version int) {
	t.Helper()
	imports, err := a.Adoption.Imports(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	wanted := make([]wow.Pin, 0, len(imports)+1)
	for _, imported := range imports {
		wanted = append(wanted, wow.Pin{Name: imported.Name, Version: imported.Version, Disabled: imported.Disabled})
	}
	wanted = append(wanted, wow.Pin{Name: name, Version: version})
	pins, err := a.Ways.ResolvePins(t.Context(), wanted)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Adoption.PutImports(t.Context(), projectID, pins); err != nil {
		t.Fatal(err)
	}
}

func adoptReq(t *testing.T, a *App, slug, body string) (int, string) {
	t.Helper()
	w := tgReq(httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle).Adopt, "POST", "/api/v1/projects/"+slug+"/adoption/adopt", body, map[string]string{"slug": slug})
	return w.Code, w.Body.String()
}

// §11: prepare says what would change and never touches the project;
// adopt is one transaction under the project's lock, refused if the
// project moved since; the rows and the receipt agree afterwards.
func TestPrepareThenAdopt(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	a.SyncStock(t.Context())
	pid, _, _ := fleetRepo(t, a, "tidy", map[string]string{"main.go": "package main\n"})
	a.ensurePlatformGuidanceFor(pid)
	recordFixtureAdoptions(t, a)
	adoptTestPlatform(t, a, pid)
	testPack(t, a, "house-style", []string{"prose/terse-style"})
	// A stock workflow moves: the apptown pack that carries it gets a new
	// version; nobody moves until they adopt it.
	entries, err := a.Ways.Workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var landV int
	var landDescription string
	for _, entry := range entries {
		if entry.Name == "land" {
			landV = entry.Version
			landDescription = entry.Doc.Metadata.Description
			break
		}
	}
	if landV == 0 {
		t.Fatal("land workflow was not listed")
	}
	_, y, err := a.Ways.WorkflowVersion(t.Context(), "land", landV)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := wow.NewWorkflows(a.DB).StockUpdate(t.Context(), "land", landDescription, y+"\n# moved\n"); err != nil || version != landV+1 {
		t.Fatalf("publish moved land: version=%d err=%v", version, err)
	}
	if _, err := a.Ways.CutPacksOnPinChange(t.Context()); err != nil {
		t.Fatal(err)
	}
	forgePack, err := a.Ways.Pack(t.Context(), "apptown")
	if err != nil {
		t.Fatal(err)
	}
	if forgePack.LatestVersion != 2 {
		t.Fatalf("a moved workflow cuts the pack that carries it: forge v%d", forgePack.LatestVersion)
	}
	if rw, _ := a.lifecycle.SelectedWorkflow(t.Context(), a.projectByID(context.Background(), pid), "land"); rw.Version != landV {
		t.Fatalf("the project stays on land v%d until it adopts: v%d", landV, rw.Version)
	}

	w := tgReq(httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle).Prepare, "POST", "/api/v1/projects/tidy/adoption/prepare", `{"packs":[{"pack":"apptown","version":2},{"pack":"house-style","version":1}]}`, map[string]string{"slug": "tidy"})
	var c adoption.Candidate
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 200 || len(c.Problems) != 0 || !containsString(c.Added, "prose/terse-style@1") || !containsString(c.LanesAdded, "quick-change") || c.ExpectedRevision != 2 || !containsString(c.Changed, fmt.Sprintf("workflow land v%d→v%d", landV, landV+1)) {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	if currentAdoption(a.DB, pid).Revision != 2 {
		t.Fatal("prepare never touches the project")
	}
	// A stale expectation is refused; the right one adopts.
	if code, body := adoptReq(t, a, "tidy", `{"expectedRevision":7,"packs":[{"pack":"apptown","version":2},{"pack":"house-style","version":1}]}`); code != 409 {
		t.Fatalf("stale: %d %s", code, body)
	}
	code, body := adoptReq(t, a, "tidy", `{"expectedRevision":2,"packs":[{"pack":"apptown","version":2},{"pack":"house-style","version":1}]}`)
	if code != 200 || !strings.Contains(body, `"revision":3`) {
		t.Fatalf("adopt: %d %s", code, body)
	}
	if rw, _ := a.lifecycle.SelectedWorkflow(t.Context(), a.projectByID(context.Background(), pid), "land"); rw.Version != landV+1 {
		t.Fatalf("adopted: the project runs land v%d now: v%d", landV+1, rw.Version)
	}
	ad := currentAdoption(a.DB, pid)
	if ad.Kind != adoption.AdoptionAdopt || ad.Revision != 3 {
		t.Fatalf("one revision: %+v", ad)
	}
	on := map[string]int64{}
	for _, pp := range testProjectPacks(t, a, pid) {
		on[pp.Pack] = pp.Version
	}
	if on["house-style"] != 1 || on["apptown"] != 2 || !laneOn(t, a.Adoption, pid, "quick-change") {
		t.Fatalf("on both packs with the lane: %v", on)
	}
	if drift := a.VerifyAdoptions(t.Context()); len(drift) != 0 {
		t.Fatalf("rows and receipt agree: %v", drift)
	}
	// Adopting again with nothing new: a fresh revision is not minted for nothing.
	code, body = adoptReq(t, a, "tidy", `{"expectedRevision":3}`)
	if code != 200 {
		t.Fatalf("no-op adopt: %d %s", code, body)
	}
}

// Two packs pinning different versions of one block, or a declared
// contradiction, are problems prepare names; nothing is chosen silently.
// Dropping a gating block a selected pack carries takes the standards
// authority.
func TestPrepareNamesProblemsAndWeakening(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	a.SyncStock(t.Context())
	pid, _, _ := fleetRepo(t, a, "p", map[string]string{"main.go": "package main\n"})
	a.ensurePlatformGuidanceFor(pid)
	recordFixtureAdoptions(t, a)
	adoptTestPlatform(t, a, pid)
	// The stock SQLite block is no longer shipped; a block of that name is
	// what the networked one contradicts.
	if _, err := a.Ways.CreateBlock(t.Context(), "data/sqlite-everywhere", "Use SQLite.", "data", "bls"); err != nil {
		t.Fatal(err)
	}
	testPack(t, a, "sqlite-house", []string{"data/sqlite-everywhere"})
	testPack(t, a, "postgres-house", []string{"data/networked-database"})
	w := tgReq(httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle).Prepare, "POST", "/api/v1/projects/p/adoption/prepare", `{"packs":[{"pack":"apptown","version":1},{"pack":"sqlite-house","version":1},{"pack":"postgres-house","version":1}]}`, map[string]string{"slug": "p"})
	if !strings.Contains(w.Body.String(), "contradicts") {
		t.Fatalf("a contradiction is a problem: %s", w.Body.String())
	}
	// A block pinned at two versions by two packs.
	terse, err := a.Ways.BlockByName(t.Context(), "prose/terse-style")
	if err != nil {
		t.Fatal(err)
	}
	testPack(t, a, "a-house", []string{"prose/terse-style"})
	if err := a.Ways.BumpBlock(t.Context(), terse.ID, "newer", "[]", "fixture", terse.Name); err != nil {
		t.Fatal(err)
	}
	testPack(t, a, "b-house", []string{"prose/terse-style"})
	w = tgReq(httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle).Prepare, "POST", "/api/v1/projects/p/adoption/prepare", `{"packs":[{"pack":"apptown","version":1},{"pack":"a-house","version":1},{"pack":"b-house","version":1}]}`, map[string]string{"slug": "p"})
	if !strings.Contains(w.Body.String(), "the packs disagree") {
		t.Fatalf("two versions of one block is the authors' problem: %s", w.Body.String())
	}
	// Adopting a pack with gating checks is a project's own choice; dropping
	// it afterwards weakens what the project is held to and needs the
	// standards authority (tgReq's actor holds none).
	code, body := adoptReq(t, a, "p", `{"expectedRevision":2,"packs":[{"pack":"apptown","version":1},{"pack":"postgres-house","version":1}]}`)
	if code != 200 {
		t.Fatalf("adopt a strict pack: %d %s", code, body)
	}
	// The test harness runs with open auth, where every actor holds every
	// capability, so the 403 cannot be shown here; what can is that the
	// candidate names the weakening the handler checks the authority for.
	w = tgReq(httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle).Prepare, "POST", "/api/v1/projects/p/adoption/prepare", `{"packs":[{"pack":"apptown","version":1}]}`, map[string]string{"slug": "p"})
	var c adoption.Candidate
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if len(c.Weakens) != 1 || c.Weakens[0] != "data/networked-database" || !containsString(c.Removed, "data/networked-database@2") && !strings.Contains(w.Body.String(), "data/networked-database@") {
		t.Fatalf("dropping a gating block is named as weakening: %s", w.Body.String())
	}
}

// When adopting needs work, an ordinary Change carries the target: its
// runs read the target, its acceptance is the checks the target brings,
// the landing is refused before the merge if the project moved, and a
// landing moves the project — once.
func TestAdoptViaChangeLandsAndFlips(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	a.SyncStock(t.Context())
	pid, _, _ := fleetRepo(t, a, "sq", map[string]string{"go.mod": "module sq\n\nrequire modernc.org/sqlite v1.0.0\n", "main.go": "package main\n"})
	a.ensurePlatformGuidanceFor(pid)
	recordFixtureAdoptions(t, a)
	adoptTestPlatform(t, a, pid)
	testPack(t, a, "networked", []string{"data/networked-database"})
	code, body := adoptReq(t, a, "sq", `{"expectedRevision":2,"viaChange":true,"packs":[{"pack":"apptown","version":1},{"pack":"networked","version":1}]}`)
	if code != 200 || !strings.Contains(body, `"changeId":`) {
		t.Fatalf("via change: %d %s", code, body)
	}
	var chID ids.ID
	var created struct {
		ChangeID ids.ID `json:"changeId"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	chID = created.ChangeID
	if chID.IsZero() {
		t.Fatal("uplift change was not listed")
	}
	ch, _ := a.changeByID(context.Background(), chID)
	if ch.TargetAdoptionID.IsZero() || ch.ExpectedRevision != 2 || !strings.Contains(ch.AcceptanceJSON, "no-file-database") || !strings.Contains(ch.Body, "no-file-database") {
		t.Fatalf("the change carries its target and acceptance: %+v", ch)
	}
	if currentAdoption(a.DB, pid).Revision != 2 {
		t.Fatal("nothing moves before the landing")
	}
	// A second one is refused while this is open.
	if code, body := adoptReq(t, a, "sq", `{"expectedRevision":2,"viaChange":true,"packs":[{"pack":"apptown","version":1}]}`); code != 409 {
		t.Fatalf("one open adoption change: %d %s", code, body)
	}
	// Its runs read the target; the target's checks are judged outright.
	doc, _ := schema.ParseWorkflowStored([]byte(lifecycleYAML))
	doc.Spec.Role = "landing"
	installFakeGit(t, a, nil)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "flow", Version: 1, Origin: "lib/flow@1"}, Spec: doc.Spec})
	run, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: chID, Workflow: "flow"})
	if err != nil {
		t.Fatal(err)
	}
	if run.AdoptionID != ch.TargetAdoptionID {
		t.Fatalf("the run reads the target: %d vs %d", run.AdoptionID, ch.TargetAdoptionID)
	}
	if !workflow.AcceptanceScopeOf(ch).Covers("guidance|data/networked-database@2|no-file-database|") {
		t.Fatal("acceptance checks are judged outright")
	}
	// The project moves on before the landing: the merge is refused.
	terse, _ := a.blockByName(t, "prose/terse-style")
	appendTestGuidance(t, a, pid, terse.Name, terse.LatestVersion)
	a.lifecycle.NoteAdoption(t.Context(), pid, "someone edited", "bls")
	if why := mergeSourceForTest(t, a, run).UpliftRefusal(); !strings.Contains(why, "moved from revision 2 to 3") {
		t.Fatalf("refused before the merge: %q", why)
	}
	// Prepared again from where the project is, it lands and flips — once.
	if _, err := a.Work.Abandon(asPerson("bls"), workflow.StopWork{ChangeID: chID, Reason: "superseded by a fresh adoption target"}); err != nil {
		t.Fatal(err)
	}
	code, body = adoptReq(t, a, "sq", `{"expectedRevision":3,"viaChange":true,"packs":[{"pack":"apptown","version":1},{"pack":"networked","version":1}]}`)
	if code != 200 {
		t.Fatalf("again: %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	chID = created.ChangeID
	if chID.IsZero() {
		t.Fatal("in-progress uplift change was not listed")
	}
	newRun, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: chID, Workflow: "flow"})
	if err != nil {
		t.Fatal(err)
	}
	if why := mergeSourceForTest(t, a, newRun).UpliftRefusal(); why != "" {
		t.Fatalf("may merge: %s", why)
	}
	for range 2 {
		snapshot, err := a.Work.Snapshot(asPerson("bls"), newRun.ID, workflow.DetailView)
		if err != nil {
			t.Fatal(err)
		}
		var approval ids.ID
		for _, step := range snapshot.Steps {
			if step.Stage == snapshot.Run.CurrentStage && step.Type == "approval" {
				approval = step.ID
			}
		}
		if approval.IsZero() {
			t.Fatalf("active approval missing: %+v", snapshot.Steps)
		}
		if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: approval, Decision: workflow.VoteApprove, Adds: shownAdds(t, a, asPerson("bls"), approval)}); err != nil {
			t.Fatal(err)
		}
	}
	runQueuedJobForTest(t, a, workflowCompletionJob, fmt.Sprintf("%d/adopt", newRun.ID))
	cur := currentAdoption(a.DB, pid)
	if cur.Kind != adoption.AdoptionAdopt || cur.Revision != 4 {
		t.Fatalf("the landing flips: %+v", cur)
	}
	ch, _ = a.changeByID(context.Background(), chID)
	if ch.State != workflow.ChangeShipped || ch.AdoptedRevision != 4 {
		t.Fatalf("the change records the revision it moved the project to: %+v", ch)
	}
	upliftSettlementFor(a).Complete(t.Context(), chID)
	a.RecoverAdoptions()
	if currentAdoption(a.DB, pid).Revision != 4 {
		t.Fatal("never twice")
	}
	onIt := false
	for _, pp := range testProjectPacks(t, a, pid) {
		if pp.Pack == "networked" {
			onIt = true
		}
	}
	if !onIt || len(a.VerifyAdoptions(t.Context())) != 0 {
		t.Fatal("on the pack, rows and receipt agreeing")
	}
}

// Pins in the first, blocks-only shape are carried forward, and a version
// AppTown cut for nothing (same contents as the one before, nobody on
// it) is removed.
// A project's own block (an addition) keeps the version it adopted until
// asked to move; the ask names the version: "block@6". A version that
// does not exist is a problem, not a silent no-op.
func TestPrepareMovesAnAdditionToANamedVersion(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	a.SyncStock(t.Context())
	pid, _, _ := fleetRepo(t, a, "own", map[string]string{"main.go": "package main\n"})
	a.ensurePlatformGuidanceFor(pid)
	terse, err := a.Ways.BlockByName(t.Context(), "prose/terse-style")
	if err != nil {
		t.Fatal(err)
	}
	bid, latest := terse.ID, terse.LatestVersion
	appendTestGuidance(t, a, pid, "prose/terse-style", latest)
	recordFixtureAdoptions(t, a)
	adoptTestPlatform(t, a, pid)
	// The library moves on; the project does not.
	if err := a.Ways.BumpBlock(t.Context(), bid, "newer", "[]", "fixture", "prose/terse-style"); err != nil {
		t.Fatal(err)
	}
	same := tgReq(httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle).Prepare, "POST", "/api/v1/projects/own/adoption/prepare", `{"packs":[{"pack":"apptown","version":1}],"additions":["prose/terse-style"]}`, map[string]string{"slug": "own"})
	var c adoption.Candidate
	_ = json.Unmarshal(same.Body.Bytes(), &c)
	if same.Code != 200 || containsString(c.Changed, fmt.Sprintf("prose/terse-style v%d→v%d", latest, latest+1)) {
		t.Fatalf("an addition named without a version keeps the one it holds: %d %s", same.Code, same.Body.String())
	}
	moved := tgReq(httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle).Prepare, "POST", "/api/v1/projects/own/adoption/prepare", fmt.Sprintf(`{"packs":[{"pack":"apptown","version":1}],"additions":["prose/terse-style@%d"]}`, latest+1), map[string]string{"slug": "own"})
	_ = json.Unmarshal(moved.Body.Bytes(), &c)
	if moved.Code != 200 || len(c.Problems) != 0 || !containsString(c.Changed, fmt.Sprintf("prose/terse-style v%d→v%d", latest, latest+1)) {
		t.Fatalf("block@N moves the addition: %d %s", moved.Code, moved.Body.String())
	}
	bad := tgReq(httpapi.NewAdoptionCommands(a.Adoption, a.conformance, a.Projects, a.Access, a.Work, a.lifecycle).Prepare, "POST", "/api/v1/projects/own/adoption/prepare", `{"packs":[{"pack":"apptown","version":1}],"additions":["prose/terse-style@99"]}`, map[string]string{"slug": "own"})
	_ = json.Unmarshal(bad.Body.Bytes(), &c)
	if len(c.Problems) == 0 || !strings.Contains(c.Problems[0], "no version 99") {
		t.Fatalf("a version that does not exist is a problem: %s", bad.Body.String())
	}
}

// Opening an adoption Change starts its working run: the Change's working
// step makes the new checks hold (or passes empty), so a Change left with
// no run waited for someone to pick a workflow (live, 21 Oct: ten sat idle).
// A run admitted and then failed is reported as that run with its error,
// never as "not started" (it exists, and blocks another start).
func TestOpeningAnAdoptionChangeStartsItsWorkingRun(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		git     bool
		started bool
	}{{"its workspace prepares", true, true}, {"its workspace fails", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			testAdministrator(t, a, "bls")
			a.SyncStock(t.Context())
			pid, _, _ := fleetRepo(t, a, "sq", map[string]string{"go.mod": "module sq\n\nrequire modernc.org/sqlite v1.0.0\n", "main.go": "package main\n"})
			a.ensurePlatformGuidanceFor(pid)
			recordFixtureAdoptions(t, a)
			adoptTestPlatform(t, a, pid)
			testPack(t, a, "networked", []string{"data/networked-database"})
			if err := a.lifecycle.SelectLane(t.Context(), pid, "quick-change", true, "fixture"); err != nil {
				t.Fatal(err)
			}
			if c.git {
				installFakeGit(t, a, nil)
			} else {
				// The run is admitted, then preparing its workspace fails.
				installFakeGit(t, a, func(workflow.CloneRequest) (workflow.WorkspaceResult, error) {
					return workflow.WorkspaceResult{}, errors.New("clone refused")
				})
			}
			code, body := adoptReq(t, a, "sq", `{"viaChange":true,"packs":[{"pack":"apptown","version":1},{"pack":"networked","version":1}]}`)
			var opened struct {
				ChangeID   ids.ID `json:"changeId"`
				RunID      ids.ID `json:"runId"`
				Workflow   string `json:"workflow"`
				RunError   string `json:"runError"`
				NotStarted string `json:"notStarted"`
			}
			if code != 200 || json.Unmarshal([]byte(body), &opened) != nil || opened.ChangeID.IsZero() {
				t.Fatalf("via change: %d %s", code, body)
			}
			if opened.RunID.IsZero() || opened.Workflow != "quick-change" || opened.NotStarted != "" {
				t.Fatalf("the adoption Change's working run: %s", body)
			}
			if c.started != (opened.RunError == "") {
				t.Fatalf("an admitted run that did not get going keeps the run and says why: %s", body)
			}
			snap, err := a.Work.Snapshot(asPerson("bls"), opened.RunID, workflow.DetailView)
			if err != nil || snap.Run.ChangeID != opened.ChangeID || snap.Run.WorkflowName != "quick-change" {
				t.Fatalf("the run is quick-change on the adoption Change: %+v %v", snap.Run, err)
			}
			// Stop the stock lane's agent work before the app stops.
			if _, err := a.Work.Abandon(asPerson("bls"), workflow.StopWork{ChangeID: opened.ChangeID, Reason: "test"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
