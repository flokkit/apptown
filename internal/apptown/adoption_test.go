package apptown

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/complaint"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Slice one of design/subsystems/ways-of-working-versions.md: a project's way of
// working is written down as a revision, releases no longer move it, a
// person's edit records the next revision, and drift outside the door is
// seen.
func TestAdoptionFreezesWhatAProjectIsOn(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.SyncStock(t.Context())
	projectID := plainTestProject(t, a.DB, "p", "P")
	if err := a.Projects.SetDefaultBranch(context.Background(), projectID, "main"); err != nil {
		t.Fatal(err)
	}
	p, _ := a.Projects.BySlug(context.Background(), "p")
	terse, _ := a.blockByName(t, "prose/terse-style")
	if err := a.Adoption.PutImports(t.Context(), p.ID, []wow.ResolvedPin{{BlockID: terse.ID, Name: terse.Name, Version: terse.LatestVersion}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Adoption.SelectLane(t.Context(), p.ID, "quick-change", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	recordFixtureAdoptions(t, a)
	ad := currentAdoption(a.DB, p.ID)
	if ad == nil || ad.Revision != 1 || ad.Kind != adoption.AdoptionEdit || len(ad.Receipt.Guidance) != 1 || ad.Digest == "" {
		t.Fatalf("the snapshot is revision 1: %+v", ad)
	}
	landBefore := 0
	for _, w := range ad.Receipt.Workflows {
		if w.Name == "land" {
			landBefore = w.Version
		}
	}
	if landBefore == 0 {
		t.Fatalf("the receipt pins the system workflows: %+v", ad.Receipt.Workflows)
	}
	recordFixtureAdoptions(t, a) // idempotent: only projects without a revision
	if currentAdoption(a.DB, p.ID).Revision != 1 {
		t.Fatal("a second boot does not snapshot again")
	}

	// A release: the block and the land workflow move on in the library;
	// the project does not.
	body, _ := a.Ways.BlockVersionBody(t.Context(), terse.ID, terse.LatestVersion)
	if err := a.Ways.BumpBlock(t.Context(), terse.ID, body+"\nA new sentence.\n", "", "apptown-system", terse.Name); err != nil {
		t.Fatal(err)
	}
	workflows, err := a.Ways.Workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var latest int
	var landDescription string
	for _, entry := range workflows {
		if entry.Name == "land" {
			latest = entry.Version
			landDescription = entry.Doc.Metadata.Description
			break
		}
	}
	if latest == 0 {
		t.Fatal("land workflow was not listed")
	}
	_, landYAML, err := a.Ways.WorkflowVersion(t.Context(), "land", latest)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := wow.NewWorkflows(a.DB).StockUpdate(t.Context(), "land", landDescription, landYAML+"\n# newer\n"); err != nil || version != latest+1 {
		t.Fatalf("publish newer land: version=%d err=%v", version, err)
	}
	if drifted := a.VerifyAdoptions(t.Context()); len(drifted) != 0 {
		t.Fatalf("a release is not drift: %v", drifted)
	}
	imports, err := a.Adoption.Imports(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pin int
	for _, imported := range imports {
		if imported.BlockID == terse.ID {
			pin = imported.Version
			break
		}
	}
	if pin != terse.LatestVersion {
		t.Fatalf("the import stays at %d, moved to %d", terse.LatestVersion, pin)
	}
	rw, err := a.lifecycle.SelectedWorkflow(t.Context(), &p, "land")
	if err != nil {
		t.Fatal(err)
	}
	if rw.Version != landBefore {
		t.Fatalf("the project runs the land it adopted (v%d), not the library's newest (v%d)", landBefore, rw.Version)
	}
	if _, v, err := a.Adoption.ResolveBlock(t.Context(), adoption.Source{ProjectID: p.ID, Revision: currentAdoption(a.DB, p.ID)}, "skill/implementation"); err == nil {
		skill, err := a.Ways.BlockByName(t.Context(), "skill/implementation")
		if err != nil {
			t.Fatal(err)
		}
		if v > skill.LatestVersion {
			t.Fatalf("a skill resolves as of the revision: %d > %d", v, skill.LatestVersion)
		}
	}

	// A person's edit is the adoption door: the next revision, with a reason.
	w := tgReq(httpapi.NewGuidance(a.Ways, a.Adoption, a.Projects, a.lifecycle, httpapi.NewSteeringInventory(a.steering, piHomeFor(a), httpapi.PlatformSteering{Complaints: complaint.AgentInstructions, Hygiene: agentenv.WorkspaceHygiene, Runtime: agentenv.RuntimeContractInstructions, Docker: agentenv.DockerInstructions})).PutProjectGuidance, "PUT", "/api/v1/projects/p/guidance",
		`{"expectedRevision":1,"imports":[{"name":"prose/terse-style"},{"name":"arch/boring-technology"}]}`, map[string]string{"slug": "p"})
	if w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	ad = currentAdoption(a.DB, p.ID)
	if ad.Revision != 2 || ad.Kind != adoption.AdoptionEdit || len(ad.Receipt.Guidance) != 2 || !strings.Contains(ad.Reason, "Guidance") {
		t.Fatalf("the edit is revision 2: %+v", ad)
	}
	// A lane selected after the revision resolves as of the revision's time,
	// never to a version published later: a moment passes on AppTown's
	// clock, which stamps both.
	moveClock(a, time.Second)
	if err := a.lifecycle.SelectLane(t.Context(), p.ID, "fix-bug", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	if currentAdoption(a.DB, p.ID).Revision != 3 {
		t.Fatal("selecting a lane is an edit: revision 3")
	}

	// Drift: something moved the rows outside the door. The rows are wrong,
	// not the receipt: the revision stays, the project carries a note, and
	// the next save through the door clears it.
	before := currentAdoption(a.DB, p.ID).Revision
	imports, err = a.Adoption.Imports(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	pins := make([]wow.ResolvedPin, 0, len(imports))
	for _, entry := range imports {
		version := entry.Version
		if entry.BlockID == terse.ID {
			version++
		}
		pins = append(pins, wow.ResolvedPin{BlockID: entry.BlockID, Name: entry.Name, Version: version, Disabled: entry.Disabled})
	}
	if err := a.Adoption.PutImports(t.Context(), p.ID, pins); err != nil {
		t.Fatal(err)
	}
	drifted := a.VerifyAdoptions(t.Context())
	if len(drifted) != 1 || drifted[0] != "p" {
		t.Fatalf("drift is seen: %v", drifted)
	}
	if currentAdoption(a.DB, p.ID).Revision != before {
		t.Fatal("drift is quarantined, never promoted")
	}
	w = tgReq(httpapi.NewProjectAdoption(a.Projects, a.Adoption, a.Ways, a.DB).ProjectAdoption, "GET", "/api/v1/projects/p/adoption", "", map[string]string{"slug": "p"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"drift"`) || !strings.Contains(w.Body.String(), "prose/terse-style") || !strings.Contains(w.Body.String(), `"edit"`) {
		t.Fatalf("the API shows the drift as a condition and the history: %d %s", w.Code, w.Body.String())
	}
	a.lifecycle.NoteAdoption(t.Context(), p.ID, "saved again", "bls")
	current, err := a.Adoption.Current(t.Context(), p.ID)
	if err != nil || current.Drift != "" || current.Revision != before+1 {
		t.Fatal("a save through the door records what it is on and clears the note")
	}
}

// §9 step 2: a run reads one revision, resolved at creation; a child
// inherits it; steering, the gate's checks and apptown check read it, so an
// edit to the project after the run started changes nothing for it.
func TestARunReadsTheRevisionItStartedWith(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "alice")
	a.SyncStock(t.Context())
	projectID := plainTestProject(t, a.DB, "p", "P")
	if err := a.Projects.SetDefaultBranch(context.Background(), projectID, "main"); err != nil {
		t.Fatal(err)
	}
	p, _ := a.Projects.BySlug(context.Background(), "p")
	terse, _ := a.blockByName(t, "prose/terse-style")
	if err := a.Adoption.PutImports(t.Context(), p.ID, []wow.ResolvedPin{{BlockID: terse.ID, Name: terse.Name, Version: terse.LatestVersion}}); err != nil {
		t.Fatal(err)
	}
	recordFixtureAdoptions(t, a)
	r1 := currentAdoption(a.DB, p.ID)

	doc, err := schema.ParseWorkflowStored([]byte(lifecycleYAML))
	if err != nil {
		t.Fatal(err)
	}
	// A declared call gives the child a real invocation identity.
	doc.Spec.Stages[0].Steps = append(doc.Spec.Stages[0].Steps, schema.Step{Name: "child", Uses: "workflow/call"})
	childSpec := schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	installFakeGit(t, a, nil)
	defineWorkflows(t, a,
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "flow", Version: 1, Origin: "lib/flow@1"}, Spec: doc.Spec},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "child", Version: 1}, Spec: childSpec})
	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "A change"})
	if err != nil {
		t.Fatal(err)
	}
	chID := change.ID
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: chID, Workflow: "flow"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}

	if run.AdoptionID != r1.ID {
		t.Fatalf("a root run resolves the current revision: %d vs %d", run.AdoptionID, r1.ID)
	}
	ad, _ := adoption.ProjectAdoptionIn(context.Background(), a.DB, run.ProjectID, run.AdoptionID)
	before, err := a.Adoption.Resolve(ctx, adoption.Source{ProjectID: run.ProjectID, Revision: ad})
	textBefore := before.Text
	if err != nil || ad == nil || ad.Revision != 1 || !strings.Contains(textBefore, "prose/terse-style") {
		t.Fatalf("steering from the receipt: %v %+v", err, ad)
	}

	// The project moves on: a block with a check is added.
	w := tgReq(httpapi.NewGuidance(a.Ways, a.Adoption, a.Projects, a.lifecycle, httpapi.NewSteeringInventory(a.steering, piHomeFor(a), httpapi.PlatformSteering{Complaints: complaint.AgentInstructions, Hygiene: agentenv.WorkspaceHygiene, Runtime: agentenv.RuntimeContractInstructions, Docker: agentenv.DockerInstructions})).PutProjectGuidance, "PUT", "/api/v1/projects/p/guidance",
		`{"expectedRevision":1,"imports":[{"name":"prose/terse-style"},{"name":"data/networked-database"}]}`, map[string]string{"slug": "p"})
	if w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if currentAdoption(a.DB, p.ID).Revision != 2 {
		t.Fatal("revision 2")
	}
	run, _ = workflow.ReadRunIn(context.Background(), a.DB, run.ID)
	ad, _ = adoption.ProjectAdoptionIn(context.Background(), a.DB, run.ProjectID, run.AdoptionID)
	after, _ := a.Adoption.Resolve(ctx, adoption.Source{ProjectID: run.ProjectID, Revision: ad})
	textAfter := after.Text
	if ad.Revision != 1 || textAfter != textBefore {
		t.Fatalf("the run still reads revision 1: r%d", ad.Revision)
	}
	checks, err := verification.ChecksOfReceipt(ctx, a.Adoption, ad)
	if err != nil || ad.Revision != 1 {
		t.Fatalf("checks from the receipt: %v r%d", err, ad.Revision)
	}
	for _, c := range checks {
		if c.Block == "data/networked-database" {
			t.Fatalf("a check adopted after the run started is not this run's: %+v", c)
		}
	}
	ch, _ := a.changeByID(ctx, chID)
	changeRun, err := workflow.ReadRunIn(context.Background(), a.DB, ch.RunID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := adoption.ProjectAdoptionIn(context.Background(), a.DB, ch.ProjectID, changeRun.AdoptionID)
	if got == nil || got.Revision != 1 {
		t.Fatalf("apptown check reads the change's run's revision: %+v", got)
	}

	// A callable pipeline child inherits, whatever the project is on now.
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var callID ids.ID
	for _, step := range snapshot.Steps {
		if step.Type == "workflow/call" {
			callID = step.ID
		}
	}
	child, err := a.Work.Call(ctx, workflow.WorkflowCall{ParentStepID: callID, Workflow: "child"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}

	if child.AdoptionID != r1.ID {
		t.Fatalf("a child inherits its parent's revision: %d vs %d", child.AdoptionID, r1.ID)
	}
	// A new root run reads revision 2.
	freshChange, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "Next change"})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: freshChange.ID, Workflow: "flow"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}

	if fresh.AdoptionID == r1.ID {
		t.Fatal("a new root run resolves the current revision")
	}
	if resolved := workEventCount(t, a, runEventKind(run.ID, "adoption.resolved")); resolved == 0 {
		t.Fatalf("the run's record names the revision it resolved")
	}
}

// §9 step 4 placement: a project whose blocks and lanes match a published
// pack version exactly is placed on it and on the apptown pack; what it
// carries beyond them is an addition, a pack block it disabled an
// exclusion, and two contradicting blocks in force are a condition.
func TestExplicitPackAdoptionAndReceiptView(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.SyncStock(t.Context())
	projectID := plainTestProject(t, a.DB, "p", "P")
	if err := a.Projects.SetDefaultBranch(context.Background(), projectID, "main"); err != nil {
		t.Fatal(err)
	}
	p, _ := a.Projects.BySlug(context.Background(), "p")
	recordFixtureAdoptions(t, a)
	var taken []*wow.Pack
	for _, name := range []string{"server-base", "go", "postgres"} {
		pack, err := a.Ways.Pack(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		taken = append(taken, pack)
	}
	if _, err := a.lifecycle.ApplyPack(adoption.WithSystemAuthority(context.Background(), p.ID), &p, taken, "bls"); err != nil {
		t.Fatal(err)
	}
	packs := testProjectPacks(t, a, p.ID)
	names := map[string]int64{}
	for _, pp := range packs {
		names[pp.Pack] = pp.Version
	}
	if names["apptown"] == 0 || names["server-base"] == 0 || names["go"] == 0 || names["postgres"] == 0 {
		t.Fatalf("on apptown, server-base, go and postgres: %+v", packs)
	}
	view := testPlacement(t, a, p.Slug)
	if len(view.Additions) != 0 || len(view.Exclusions) != 0 || len(view.Conditions) != 0 {
		t.Fatalf("an exact match carries nothing extra: %+v", view)
	}
	// The wizard never offers the apptown pack.
	for _, pk := range mustRecords(a.Ways.OfferedPacks(t.Context())) {
		if pk.Name == "apptown" {
			t.Fatal("the platform pack is not a choice")
		}
	}

	// A person adds the SQLite block beside the Postgres one, and disables
	// a pack block: an addition, an exclusion, and a contradiction.
	imports, _ := a.Adoption.Imports(t.Context(), p.ID)
	var body []string
	for _, im := range imports {
		dis := "false"
		if im.Name == "prose/terse-style" {
			dis = "true"
		}
		body = append(body, `{"name":"`+im.Name+`","version":`+itoa(int64(im.Version))+`,"disabled":`+dis+`}`)
	}
	// The stock SQLite block is no longer shipped; a block of that name is
	// what the networked one contradicts.
	if _, err := a.Ways.CreateBlock(t.Context(), "data/sqlite-everywhere", "Use SQLite.", "data", "bls"); err != nil {
		t.Fatal(err)
	}
	body = append(body, `{"name":"data/sqlite-everywhere"}`)
	w := tgReq(httpapi.NewGuidance(a.Ways, a.Adoption, a.Projects, a.lifecycle, httpapi.NewSteeringInventory(a.steering, piHomeFor(a), httpapi.PlatformSteering{Complaints: complaint.AgentInstructions, Hygiene: agentenv.WorkspaceHygiene, Runtime: agentenv.RuntimeContractInstructions, Docker: agentenv.DockerInstructions})).PutProjectGuidance, "PUT", "/api/v1/projects/p/guidance", `{"expectedRevision":`+itoa(currentAdoption(a.DB, p.ID).Revision)+`,"imports":[`+strings.Join(body, ",")+`]}`, map[string]string{"slug": "p"})
	if w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	view = testPlacement(t, a, p.Slug)
	if len(view.Additions) != 1 || view.Additions[0].Name != "data/sqlite-everywhere" {
		t.Fatalf("the addition: %+v", view.Additions)
	}
	if len(view.Exclusions) != 1 || view.Exclusions[0].Name != "prose/terse-style" {
		t.Fatalf("the exclusion: %+v", view.Exclusions)
	}
	if len(view.Conditions) != 1 || view.Conditions[0].Kind != "conflict" || !strings.Contains(view.Conditions[0].Sentence, "contradict") {
		t.Fatalf("the contradiction is a condition: %+v", view.Conditions)
	}
	w = tgReq(httpapi.NewProjectAdoption(a.Projects, a.Adoption, a.Ways, a.DB).ProjectAdoption, "GET", "/api/v1/projects/p/adoption", "", map[string]string{"slug": "p"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"pack":"postgres"`) || !strings.Contains(w.Body.String(), `"kind":"conflict"`) {
		t.Fatalf("the API says what the project is on: %d %s", w.Code, w.Body.String())
	}

	// A hand-assembled project: on the apptown pack alone, everything else an addition.
	handProjectID := plainTestProject(t, a.DB, "hand", "H")
	if err := a.Projects.SetDefaultBranch(context.Background(), handProjectID, "main"); err != nil {
		t.Fatal(err)
	}
	h, _ := a.Projects.BySlug(context.Background(), "hand")
	a.ensurePlatformGuidanceFor(h.ID)
	terse, _ := a.blockByName(t, "prose/terse-style")
	appendTestGuidance(t, a, h.ID, terse.Name, terse.LatestVersion)
	recordFixtureAdoptions(t, a)
	adoptTestPlatform(t, a, h.ID)
	view = testPlacement(t, a, h.Slug)
	if len(view.Packs) != 1 || view.Packs[0].Pack != "apptown" || !view.Packs[0].Platform || len(view.Additions) != 1 {
		t.Fatalf("hand-assembled: apptown alone plus additions: %+v", view)
	}
}

// SQL-heavy fixtures assemble rows directly; make their initial receipt an
// explicit setup step. Production creation writes its receipt atomically.
func recordFixtureAdoptions(t *testing.T, a *App) {
	t.Helper()
	projects, err := a.Projects.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID.Compare(projects[j].ID) < 0 })
	for i := range projects {
		p := &projects[i]
		current, err := a.Adoption.Current(t.Context(), p.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		if current != nil && current.Revision != 0 {
			continue
		}
		if _, err := a.recordAdoption(p, "edit", "explicit test configuration", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
}

// These fixtures deliberately imported the current apptown pack's contents.
// Record that declared choice; never infer packs by matching block names.
// ensurePlatformGuidanceFor adds the platform blocks a project is missing,
// appended after its own imports at the block's latest version. Returns the
// names it added. The hand-assembled fixture's way onto the platform tier
// (design/subsystems/conformance.md §13.1); the product puts a repo-backed
// project on it by adopting the apptown pack (applyPack), and a platform
// block published later reaches an existing project through adoption.
func (a *App) ensurePlatformGuidanceFor(projectID ids.ID) []string {
	missing, err := a.Adoption.PlatformBlocksMissingFrom(context.Background(), projectID)
	if err != nil {
		return nil
	}
	var added []string
	for _, b := range missing {
		if err := a.Adoption.AppendPlatformImport(context.Background(), projectID, b); err != nil {
			continue
		}
		added = append(added, fmt.Sprintf("%s@%d", b.Name, b.LatestVersion))
	}
	return added
}

// Adopt the platform through the same prepared selection used by a person.
// Preserve hand-added guidance, and scope the choice to this project.
func adoptTestPlatform(t *testing.T, a *App, projectID ids.ID) {
	t.Helper()
	pack, err := a.Ways.Pack(t.Context(), "apptown")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := a.Adoption.Prepare(t.Context(), projectID, adoption.Request{Packs: []adoption.PackPick{{Pack: pack.Name, Version: pack.LatestVersion}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.lifecycle.Adopt(adoption.WithSystemAuthority(context.Background(), projectID), a.projectByID(context.Background(), projectID), candidate, "fixture", "select platform"); err != nil {
		t.Fatal(err)
	}
}

// recordAdoption resolves the project's union and writes it as the next
// revision with its receipt, or returns the current one when nothing
// changed. The package locks the project row before the resolve; the
// resolve still reads the library through the database while lanes and
// the workflow library are AppTown's (the lock serializes writers, which is
// what matters).
func (a *App) recordAdoption(p *project.Project, kind, reason, by string) (*adoption.RecordedRevision, error) {
	k, err := adoption.ParseAdoptionKind(kind)
	if err != nil {
		return nil, err
	}
	return a.Adoption.RecordCurrent(context.Background(), p.ID, k, reason, by)
}

// A project runs the workflow version it adopted (design/ways-of-working-
// versions.md §4.1, §9 step 1; this supersedes workflows-as-lanes.md's
// "latest wins"): selecting the lane records the revision that pins it,
// and a library bump changes nothing for the project until it adopts.
// A project without a recorded revision cannot run.
func TestProjectRunsTheWorkflowVersionItAdopted(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	plainTestProject(t, a.DB, "faithful", "F")
	p, _ := a.Projects.BySlug(context.Background(), "faithful")

	v1 := strings.Replace(lifecycleYAML, "name: flow", "name: sub-flow", 1)
	v2 := strings.Replace(v1, "the direction is right", "the direction is right AND documented", 1)
	if name, version, err := a.Ways.PublishWorkflow(t.Context(), v1, "fixture"); err != nil || name != "sub-flow" || version != 1 {
		t.Fatalf("publish v1: %s@%d %v", name, version, err)
	}
	a.lifecycle.SelectLane(t.Context(), p.ID, "sub-flow", true, "fixture") // records revision 1, pinning sub-flow@1

	rw, err := a.lifecycle.SelectedWorkflow(t.Context(), &p, "sub-flow")
	if err != nil || rw.Origin != "lib/sub-flow@1" {
		t.Fatalf("v1 expected: %v %+v", err, rw)
	}
	if name, version, err := a.Ways.PublishWorkflow(t.Context(), v2, "fixture"); err != nil || name != "sub-flow" || version != 2 {
		t.Fatalf("publish v2: %s@%d %v", name, version, err)
	}
	rw, err = a.lifecycle.SelectedWorkflow(t.Context(), &p, "sub-flow")
	if err != nil || rw.Origin != "lib/sub-flow@1" || rw.YAML != v1 {
		t.Fatalf("the adopted version stays: %v %+v", err, rw)
	}
	if currentAdoption(a.DB, p.ID).Revision != 1 {
		t.Fatal("a library bump is not a revision")
	}
	// No revision must not silently select the newer library version.
	plainTestProject(t, a.DB, "old", "O")
	o, _ := a.Projects.BySlug(context.Background(), "old")
	rw, err = a.lifecycle.SelectedWorkflow(t.Context(), &o, "sub-flow")
	if err == nil || rw != nil || !strings.Contains(err.Error(), "no recorded adoption") {
		t.Fatalf("missing receipt must refuse: %v %+v", err, rw)
	}
}

// A run keeps the workflow version it was admitted under
// (compatibility.md): when its project adopts the next version while the
// run is under way, the steps the run reaches after that are still the
// admitted version's, and only a run started afterwards takes the new one.
func TestARunKeepsTheWorkflowVersionItWasAdmittedUnder(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")
	p := engineProject(t, a, "p")
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	seedLibraryWorkflow(t, a, p.ID, "flow", lifecycleYAML)
	asAppTown := a.asAppTown(context.Background(), p.ID, "test")
	admitted, err := a.Work.StartRun(asAppTown, workflow.StartChangeRun{ChangeID: engineChange(t, a, p).ID, Workflow: "flow"})
	if err != nil || admitted.WorkflowVersion != 1 {
		t.Fatalf("admitted under flow@1: %+v %v", admitted, err)
	}

	// The project adopts flow@2 while the run waits at its first approval.
	v2 := strings.Replace(strings.Replace(lifecycleYAML, "the direction is right", "the direction is right, v2", 1), "the result is right", "the result is right, v2", 1)
	if name, version, err := a.Ways.PublishWorkflow(t.Context(), v2, "fixture"); err != nil || name != "flow" || version != 2 {
		t.Fatalf("publish v2: %s@%d %v", name, version, err)
	}
	if err := a.Adoption.SelectLane(t.Context(), p.ID, "flow", false, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Adoption.SelectLane(t.Context(), p.ID, "flow", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	if rw, err := a.lifecycle.SelectedWorkflow(t.Context(), p, "flow"); err != nil || rw.Origin != "lib/flow@2" {
		t.Fatalf("the project now runs flow@2: %+v %v", rw, err)
	}

	stepConfig := func(run ids.ID, name string) string {
		t.Helper()
		snapshot, err := a.Work.Snapshot(asPerson("bls"), run, workflow.DetailView)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range snapshot.Steps {
			if step.Name == name {
				return string(step.Config)
			}
		}
		t.Fatalf("run %s has no step %s yet: %+v", run, name, snapshot.Steps)
		return ""
	}
	first := stepConfig(admitted.ID, "first-ok")
	snapshot, _ := a.Work.Snapshot(asPerson("bls"), admitted.ID, workflow.DetailView)
	for _, step := range snapshot.Steps {
		if step.Name == "first-ok" {
			if _, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: step.ID, Decision: workflow.VoteApprove}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The stage the run reaches after the adoption is the admitted version's.
	if second := stepConfig(admitted.ID, "second-ok"); strings.Contains(first+second, "v2") || !strings.Contains(second, "the result is right") {
		t.Fatalf("the admitted run took the adopted version: first %s, second %s", first, second)
	}
	fresh, err := a.Work.StartRun(asAppTown, workflow.StartChangeRun{ChangeID: engineChange(t, a, p).ID, Workflow: "flow"})
	if err != nil || fresh.WorkflowVersion != 2 || !strings.Contains(stepConfig(fresh.ID, "first-ok"), "the direction is right, v2") {
		t.Fatalf("a run started after the adoption takes flow@2: %+v %v", fresh, err)
	}
}

// currentAdoption reads the current receipt in one statement snapshot
// through q (a transaction sees what it locked). Optional callers receive
// nil when no receipt exists; malformed receipts and read errors are
// logged. Call after acquiring any project lock, never cache it across locks.
func currentAdoption(q database.Queryer, projectID ids.ID) *adoption.RecordedRevision {
	ad, err := adoption.CurrentReceiptIn(context.Background(), q, projectID)
	readOK(err)
	return ad
}

// A project resolves a guidance block as of its adoption revision's time,
// and the block's versions and the revision are stamped by one clock, the
// AppTown's: with that clock two hours behind the wall clock or two hours
// ahead of it, a block made before the revision and edited after it
// resolves, as of the revision, to the version it had then.
//
// Kills: the first version stamped by the wall clock (behind, it is later
// than the revision and nothing resolves), the revision stamped by it
// (behind, it is later than the edit), or the edit stamped by it (ahead,
// it is earlier than the revision and resolves instead).
func TestABlockResolvesAsOfItsRevisionByAppTownsClock(t *testing.T) {
	t.Parallel()
	for _, skew := range []time.Duration{-2 * time.Hour, 2 * time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			moveClock(a, skew)
			pid := plainTestProject(t, a.DB, "as-of", "As Of")
			if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
				if err := adoption.InitializeIn(t.Context(), tx, pid, 0, ""); err != nil {
					return err
				}
				return adoption.RecordIn(t.Context(), tx, pid, 1, adoption.AdoptionCreate, "fixture", adoption.EmptyReceipt(), "boss", a.cfg.clock.Now())
			}); err != nil {
				t.Fatal(err)
			}
			block, err := a.Ways.CreateBlock(t.Context(), "house/as-of", "as it was", "", "boss")
			if err != nil {
				t.Fatal(err)
			}
			revision, err := a.Adoption.Record(t.Context(), pid, adoption.AdoptionEdit, "fixture", "boss", func(*database.Tx, *adoption.RecordedRevision, time.Time) (adoption.Receipt, error) {
				return adoption.EmptyReceipt(), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			later := "as it became"
			if _, err := a.Ways.EditBlock(context.Background(), block.ID, wow.BlockPatch{BodyMD: &later}, "boss"); err != nil {
				t.Fatal(err)
			}
			got, err := a.Ways.Block(t.Context(), wow.BlockRef{ID: block.ID, AsOf: revision.CreatedAt.Time})
			if err != nil || got.Version != 1 || got.Body != "as it was" {
				t.Fatalf("as of revision %d: version %d %q %v", revision.Revision, got.Version, got.Body, err)
			}
		})
	}
}
