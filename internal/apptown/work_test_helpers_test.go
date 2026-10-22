package apptown

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

// mergeSeam: the merge commands as AppTown wires them, with a driver a test
// may wrap before the merge runs.
type mergeSeam struct {
	a      *App
	driver func() scm.Driver
}

func mergesFor(a *App) *mergeSeam       { return &mergeSeam{a: a, driver: mergeEffectsFor(a).Driver} }
func (m *mergeSeam) Driver() scm.Driver { return m.driver() }
func (m *mergeSeam) Observed(ctx context.Context, r scm.Result) {
	mergeEffectsFor(m.a).Observed(ctx, r)
}
func (m *mergeSeam) owner() *workflow.Merges {
	return workflow.NewMerges(m.a.DB, m.a.cfg.dataDir, workflowLawRules{}, scmFor(m.a), m)
}

// scmFor: the SCM service as Work builds it, over the App's database and
// repository locks, for tests that inspect merge records directly.
func scmFor(a *App) *scm.Service {
	return scm.New(a.DB, workflow.NewSCMWork(a.DB, a.cfg.dataDir, workflowLawRules{}), &a.repositories, nil)
}

// rebuildWork builds the App's Work again from its own dependencies with
// one of them replaced: fake runners, a fake git, a resolver that returns
// a fixed definition. Replacements a test made earlier for the same App
// stay in force, so fixtures compose.
func rebuildWork(t testing.TB, a *App, mutate func(*workflow.Dependencies)) {
	t.Helper()
	var mutators []func(*workflow.Dependencies)
	if previous, ok := workOverrides.Load(a); ok {
		mutators = append(mutators, previous.([]func(*workflow.Dependencies))...)
	}
	mutators = append(mutators, mutate)
	workOverrides.Store(a, mutators)
	deps := a.workDependencies()
	for _, m := range mutators {
		m(&deps)
	}
	a.Work = workflow.New(context.Background(), a.DB, deps)
	rebuildConsoleOwners(a)
	a.repositoryEdits = repositoryedit.New(a.Work, a.Dependencies, repositoryPublisher{secrets: a.Secrets, db: a.DB, dataDir: a.cfg.dataDir, now: a.cfg.clock.Now}, a.branches, a.lifecycle, a.DB, a.cfg.dataDir)
	a.changeActions = changeactions.New(a.Work, a.Ways, a.DB)
	a.ADRs = adrs.New(a.DB, access.Checked{Authority: identity.Scoped(a.Access, "adrs"), Projects: a.Projects}, refreshADREvidence(a.Work, a.asEngine(context.Background())), a.cfg.clock.Now)
	rebuildFixes(t, a)
	// The route table holds the owners it was built with.
	rebuildRoutes(a)
}

var workOverrides sync.Map // *App -> []func(*workflow.Dependencies)

// fakeGit stands in for the repositories on disk: a clone is whatever the
// test says it is, attachments and branch questions are trivially answered.
type fakeGit struct {
	clone func(workflow.CloneRequest) (workflow.WorkspaceResult, error)
}

func (g fakeGit) EnsureClone(_ context.Context, in workflow.CloneRequest) (workflow.WorkspaceResult, error) {
	if g.clone == nil {
		return workflow.WorkspaceResult{Path: in.Workspace, Tip: "fake"}, nil
	}
	return g.clone(in)
}
func (fakeGit) SyncAttachments(context.Context, ids.ID, string) error             { return nil }
func (fakeGit) BringToBranch(context.Context, string, string, string) error       { return nil }
func (fakeGit) BranchTip(context.Context, string, string) (string, error)         { return "fake", nil }
func (fakeGit) MergeBase(context.Context, string, string, string) (string, error) { return "fake", nil }
func (fakeGit) Paths() workflow.Layout                                            { return workflow.Layout{} }

// installFakeGit rebuilds Work over a fake git whose clones the test decides.
func installFakeGit(t testing.TB, a *App, clone func(workflow.CloneRequest) (workflow.WorkspaceResult, error)) {
	t.Helper()
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Git = fakeGit{clone: clone} })
}
func (m *mergeSeam) advanceContext(ctx context.Context, run *workflow.Run) (workflow.MergeResult, error) {
	return m.owner().Merge(ctx, run)
}
func (m *mergeSeam) execute(ctx context.Context, call workflow.MergeCall) (workflow.MergeObservation, error) {
	return m.owner().Execute(ctx, call)
}

// advanceWorkflowMerge: the advancement call Merges.Merge builds for itself,
// for tests that drive Execute with a driver of their own.
func advanceWorkflowMerge(run *workflow.Run, source workflow.MergeSource) workflow.MergeCall {
	return workflow.MergeCall{Purpose: workflow.MergeForAdvance, Run: *run, Source: source}
}

// --- db-only seams over Work's parts ---------------------------------------
// Production reaches every part through a.Work; these wrappers let a test
// drive one part from a bare *database.DB without building an App.

type abandonmentStore struct{ db *database.DB }

func (s abandonmentStore) abandon(in workflow.Abandonment) (*workflow.Abandoned, error) {
	return workflow.NewAbandonments(s.db, abandonmentEffects{}, &scm.RepositoryLocks{}).Abandon(context.Background(), in)
}

// advanceAs attempts a run's gate through Work's door as who: "system" is
// AppTown acting for itself; a name is a person in open mode.
func advanceAs(a *App, runID ids.ID, who string) (*workflow.AdvanceResult, error) {
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		return nil, err
	}
	ctx := a.asAppTown(context.Background(), run.ProjectID, "test")
	if who != "system" && who != "test" {
		ctx = asPerson(who)
	}
	return a.Work.Advance(ctx, runID)
}

type testActor struct{}

func (testActor) Require(ctx context.Context, _ access.Permission) (access.Caller, error) {
	c, _ := access.CallerFrom(ctx)
	return c, nil
}

// asPerson: a person acting in open mode, for tests of a person's door.
// asLander is a person who may land. Fixtures that start a landing which
// merges before any approval need change:land (workflow/assent.go).
func asLander(t *testing.T, a *App) context.Context {
	t.Helper()
	testAdministrator(t, a, "lander")
	return asPerson("lander")
}

func asPerson(who string) context.Context {
	return verifiedContext(context.Background(), access.Principal{Kind: "user", Name: who, Via: "open"})
}

// blockByName reads a guidance block by name, the test's way of asking
// what a sync or an edit did to the library.
func (a *App) blockByName(t *testing.T, name string) (*wow.Block, error) {
	t.Helper()
	return a.Ways.BlockByName(t.Context(), name)
}

// canonJSON is one spelling for a JSON value — decoded and re-encoded, so
// jsonb's spacing and key order and Go's compact form agree. "" stays "".
func canonJSON(s string) string {
	var v any
	if strings.TrimSpace(s) == "" || json.Unmarshal([]byte(s), &v) != nil {
		return strings.TrimSpace(s)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Private stage fixtures use the same executor registry as the production assembly.
func isAutoStep(kind string) bool {
	for _, k := range autoStepTypes() {
		if k == kind {
			return true
		}
	}
	return false
}

// This command-only double deliberately cannot authorize aggregate reads.
func (testActor) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, access.ErrAuthorityUnavailable
}

// shownAdds is the digest of what an approval's commit adds, as the owner's
// question shows it: an approving vote on a commit sends it back, as every
// client does after reading the question. "" when the approval assents to
// no commit.
func shownAdds(t *testing.T, a *App, ctx context.Context, stepID ids.ID) string {
	t.Helper()
	q, err := a.Work.ApprovalQuestion(ctx, stepID)
	if err != nil {
		t.Fatalf("reading what approving step %d agrees to: %v", stepID, err)
	}
	if q.Adds == nil {
		return ""
	}
	return q.Adds.Digest
}
