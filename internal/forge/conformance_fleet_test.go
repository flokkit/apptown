package forge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/forge/httpapi"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/strs"
)

// Conformance (design/subsystems/conformance.md): the read-only fleet posture
// review. These tests exercise the REAL evaluation (runShChecks against a real
// staged tree) with the check executor routed to the local shell, so no Docker
// daemon is needed.

// fleetRepo creates a repo-backed project at project-<slug> with a committed
// tree, and returns its id, the committed SHA, and a git runner for the repo.
func fleetRepo(t *testing.T, a *App, slug string, files map[string]string) (ids.ID, string, func(...string) string) {
	t.Helper()
	repo := a.projectRepoPath(slug)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		c := gittest.Command(args...)
		c.Dir = repo
		c.Env = append(c.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	// One commit of files, from one git fast-import: the seed every test's
	// fleet repository starts from.
	branch := "main"
	sha := gittest.Repository(t, repo, branch, gittest.Commit{Message: "seed", Files: files})[0]
	var projectID ids.ID
	err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
		if err != nil {
			return err
		}
		projectID = id
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Projects.SetDefaultBranch(context.Background(), projectID, branch); err != nil {
		t.Fatal(err)
	}
	return projectID, sha, git
}

// adopt pins one guidance block (with checks) as an import on a project — the
// project ADOPTS it at that version.
func adopt(t *testing.T, a *App, projectID ids.ID, block string, ver int, checksJSON string) {
	t.Helper()
	b, err := a.Ways.BlockByName(t.Context(), block)
	if errors.Is(err, sql.ErrNoRows) {
		if err = wow.NewLibrary(a.DB).StockInsert(t.Context(), wow.StockBlock{Name: block, Body: "body", ChecksJSON: checksJSON}); err != nil {
			t.Fatal(err)
		}
		b, err = a.Ways.BlockByName(t.Context(), block)
	}
	if err != nil {
		t.Fatal(err)
	}
	for b.LatestVersion < ver {
		if _, err := wow.NewLibrary(a.DB).StockUpdate(t.Context(), wow.StockBlock{Name: block, Body: "body", ChecksJSON: checksJSON}); err != nil {
			t.Fatal(err)
		}
		b, err = a.Ways.BlockByName(t.Context(), block)
		if err != nil {
			t.Fatal(err)
		}
	}
	storedChecks, err := a.Ways.BlockVersionChecks(t.Context(), b.ID, ver)
	var stored, requested []any
	storedErr := json.Unmarshal([]byte(storedChecks), &stored)
	requestedErr := json.Unmarshal([]byte(checksJSON), &requested)
	containsRequested := func() bool {
		for _, want := range requested {
			found := false
			for _, have := range stored {
				found = found || reflect.DeepEqual(have, want)
			}
			if !found {
				return false
			}
		}
		return true
	}
	if err != nil || storedErr != nil || requestedErr != nil || !containsRequested() {
		t.Fatalf("published %s v%d checks = %q, %v; want %q", block, ver, storedChecks, err, checksJSON)
	}
	ctx := adoption.WithSystemAuthority(context.Background(), projectID)
	prior, err := a.Adoption.CurrentReceipt(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		receipt := adoption.EmptyReceipt()
		receipt.Guidance = []adoption.ReceiptBlock{{Name: block, BlockID: b.ID, Version: ver}}
		candidate := adoption.Candidate{ProjectID: projectID, ExpectedRevision: 0, Additions: []string{block}, Receipt: receipt}
		candidate.Digest = adoption.Digest(candidate.Receipt)
		err = a.DB.InTx(ctx, func(tx *database.Tx) error {
			if err := adoption.InitializeIn(ctx, tx, projectID, 0, "", ""); err != nil {
				return err
			}
			_, err := a.Adoption.AdoptIn(ctx, tx, projectID, candidate, 0, "forge-system", "fixture explicitly adopts check", adoption.OperationSelect)
			return err
		})
	} else if err == nil {
		additions := make([]string, 0, len(prior.Receipt.Guidance)+1)
		for _, held := range prior.Receipt.Guidance {
			if held.Name != block {
				additions = append(additions, fmt.Sprintf("%s@%d", held.Name, held.Version))
			}
		}
		additions = append(additions, fmt.Sprintf("%s@%d", block, ver))
		candidate, prepareErr := a.Adoption.Prepare(ctx, projectID, adoption.Request{Additions: additions})
		if prepareErr != nil {
			err = prepareErr
		} else {
			_, err = a.lifecycle.Adopt(ctx, a.projectByID(ctx, projectID), candidate, "forge-system", "fixture explicitly adopts check", adoption.OperationSelect)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

const noFixmeSh = `if grep -rl FIXME . >/dev/null 2>&1; then echo "FIXME left in the tree"; exit 1; fi; exit 0`

func checksJSON(entries ...string) string {
	return "[" + strings.Join(entries, ",") + "]"
}
func shCheck(name, sh, sev string) string {
	return fmt.Sprintf(`{"name":%q,"evaluator":"sh","sh":%q,"severity":%q,"expects":"the rule holds"}`, name, sh, sev)
}
func llmCheck(name string) string {
	return fmt.Sprintf(`{"name":%q,"evaluator":"llm","rubric":"a judgement","expects":"the rule holds"}`, name)
}

// The fleet review yields the correct verdict for each posture state, keyed by
// the exact commit and expectation; not-scanned is the absence of a record.
func TestFleetReviewVerdictStates(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	testAdministrator(t, a, "tester")
	tgLocalSh(t, a)

	alpha, alphaSHA, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "all good"})
	adopt(t, a, alpha, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	beta, _, _ := fleetRepo(t, a, "beta", map[string]string{"b.txt": "// FIXME hurry"})
	adopt(t, a, beta, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	gamma, _, _ := fleetRepo(t, a, "gamma", map[string]string{"c.txt": "x"})
	adopt(t, a, gamma, "strict", 1, checksJSON(shCheck("boom", "exit 3", "high")))

	delta, _, _ := fleetRepo(t, a, "delta", map[string]string{"d.txt": "x"})
	adopt(t, a, delta, "prose", 2, checksJSON(llmCheck("tone")))

	// The same failure under an exception covering the check's fingerprint.
	epsilon, _, _ := fleetRepo(t, a, "epsilon", map[string]string{"e.txt": "// FIXME hurry"})
	adopt(t, a, epsilon, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	if _, err := a.conformance.CreateDisposition(testConformanceContext("t"), conformance.ExceptionRequest{
		ProjectID: epsilon, Fingerprint: "guidance|hygiene@1|no-fixme|", Kind: conformance.AcceptedRisk, Justification: "legacy note, tracked elsewhere",
	}); err != nil {
		t.Fatal(err)
	}

	rev, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("tester"), "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rev.Projects != 5 {
		t.Fatalf("expected 5 projects assessed, got %d", rev.Projects)
	}

	state := func(slug string) conformance.Outcome {
		for _, v := range verdicts {
			if v.ProjectSlug == slug {
				return v.State
			}
		}
		return 0
	}
	if state("alpha") != conformance.Conforms {
		t.Errorf("alpha (clean tree): %q, want conforms", state("alpha"))
	}
	if state("beta") != conformance.DoesNotConform {
		t.Errorf("beta (FIXME in tree): %q, want does-not-conform", state("beta"))
	}
	if state("gamma") != conformance.EvaluationError {
		t.Errorf("gamma (exit 3): %q, want error", state("gamma"))
	}
	// An llm rubric with no scope is a TRANSITION check (diff-scoped): a review
	// has no before/after, so it is not-applicable — never cannot-assess, never
	// silently reused from its gate form (design/subsystems/conformance-surface.md §2).
	if state("delta") != conformance.NotApplicable {
		t.Errorf("delta (diff-scoped rubric): %q, want not-applicable", state("delta"))
	}
	// An excepted failure keeps the evaluated failure underneath and names
	// the exception from the review's snapshot.
	for _, v := range verdicts {
		if v.ProjectSlug == "epsilon" && (v.State != conformance.Excepted || !strings.Contains(v.Detail, "FIXME") || !strings.Contains(v.ExceptedBy, "accepted")) {
			t.Errorf("epsilon (FIXME under an exception): %+v, want excepted keeping the failure", v)
		}
	}
	if state("epsilon") != conformance.Excepted {
		t.Errorf("epsilon: %q, want excepted", state("epsilon"))
	}
	if rev.Errors != 1 {
		t.Errorf("expected 1 errored evaluation, got %d", rev.Errors)
	}

	// not-scanned is the ABSENCE of a record, never a stored value.
	if _, err := savedLatestVerdict(a,

		alpha, "hygiene", 1, "never-ran"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absent verdict must return no rows: %v", err)
	}

	// Verdict records carry the full identity key (§2).
	v, err := savedLatestVerdict(a,

		alpha, "hygiene", 1, "no-fixme")
	if err != nil {
		t.Fatal("alpha's verdict must be on record")
	}
	if v.SHA != alphaSHA {
		t.Errorf("verdict SHA %q != committed SHA %q", v.SHA, alphaSHA)
	}
	if v.Version != 1 || v.Check != "no-fixme" || v.Evaluator != "sh" || v.Mode != "posture" {
		t.Errorf("verdict key wrong: %+v", v)
	}
	if v.EvaluatorVer == "" || v.At == "" || v.ReviewID != rev.ID {
		t.Errorf("verdict missing evaluator version / timestamp / review link: %+v", v)
	}
}

// The review evaluates the COMMITTED SHA, not the dirty workspace: an
// uncommitted violation does not change the verdict.
func TestFleetReviewIsHermetic(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)

	id, _, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "clean"})
	adopt(t, a, id, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	// Drop an uncommitted FIXME into the live workspace.
	if err := os.WriteFile(filepath.Join(a.projectRepoPath("alpha"), "dirty.txt"), []byte("FIXME uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 || verdicts[0].State != conformance.Conforms {
		t.Fatalf("committed tree is clean, so the verdict must be conforms despite the dirty file: %+v", verdicts)
	}
}

// The review is READ-ONLY outside the conformance tables: it opens no Change
// and creates no steps or gates.
func TestFleetReviewReadOnly(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)

	id, _, _ := fleetRepo(t, a, "alpha", map[string]string{"b.txt": "// FIXME"})
	adopt(t, a, id, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	count := func(table string) int {
		var n int
		_ = a.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
		return n
	}
	chBefore, stBefore := count("changes"), count("steps")

	_, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if count("changes") != chBefore || count("steps") != stBefore {
		t.Fatalf("fleet review must open no Change and no step: changes %d→%d, steps %d→%d",
			chBefore, count("changes"), stBefore, count("steps"))
	}
	if len(verdicts) == 0 || count("conformance_verdicts") == 0 {
		t.Fatal("the review should still have recorded verdicts (proving it ran)")
	}
}

// Expectations declare evaluation modes (§2): an sh check is posture-
// assessable (committed-tree), an llm-only check has no posture mode.

// A review with no verdicts yet must come back as an empty list, never nil:
// the page reads it as a list the moment a review starts.
// The fresh start removes every review-derived record in one act with a
// receipt, and refuses while a review is running.
func TestClearConformanceHistory(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	pid, _, _ := fleetRepo(t, a, "clr", map[string]string{"a.txt": "x"})
	ctx := testConformanceContext("t")
	execution, err := a.conformance.BeginReview(ctx, conformance.ReviewRequest{Trigger: "manual", Projects: []ids.ID{pid}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Fixes.ClearHistory(ctx); err == nil {
		t.Fatal("a running review must refuse the fresh start")
	}
	if _, err := execution.Finish(ctx, conformance.Completion{}, conformance.ReviewDone); err != nil {
		t.Fatal(err)
	}
	counts, err := a.Fixes.ClearHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Reviews != 1 {
		t.Fatalf("expected the one review removed, got %v", counts)
	}
	var left int
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM conformance_reviews`).Scan(&left)
	if left != 0 {
		t.Fatalf("reviews left: %d", left)
	}
	var audits int
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE kind='conformance.history-cleared'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("expected one audit receipt, got %d", audits)
	}
}

// The fresh start is a development convenience: refused unless the
// dev-tools feature is on, and unknown feature names are refused at boot.
func TestClearConformanceHistoryNeedsTheDevToolsFeature(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	req := identifiedTestRequest("POST", "/api/v1/conformance/history/clear", nil)
	rec := httptest.NewRecorder()
	httpapi.NewConformanceMaintenance(a.Fixes, a.Feature("dev-tools")).ClearConformanceHistory(rec, req.WithContext(callerContext(req)))
	if rec.Code != 404 {
		t.Fatalf("without the feature: want 404, got %d %s", rec.Code, rec.Body.String())
	}
	a = conformanceTestApp(t, Config{Features: "dev-tools"})
	rec = httptest.NewRecorder()
	httpapi.NewConformanceMaintenance(a.Fixes, a.Feature("dev-tools")).ClearConformanceHistory(rec, req.WithContext(callerContext(req)))
	if rec.Code != 200 {
		t.Fatalf("with the feature: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if err := (Config{Features: "dev-tools,no-such-thing"}).Validate(); err == nil {
		t.Fatal("an unknown feature name must be refused")
	}
}

func TestReviewExecutesInferredDeclarationsAtTheirSavedSource(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	testAdministrator(t, a, "tester")
	tgLocalSh(t, a)
	id, sha, _ := fleetRepo(t, a, "inferred", map[string]string{"a.txt": "saved evidence"})
	adopt(t, a, id, "inference", 1, `[{"name":"script","sh":"test -f a.txt"},{"name":"rubric","rubric":"a transition judgement"}]`)
	review, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("tester"), "manual", conformance.ReviewOptions{Projects: []ids.ID{id}})
	if err != nil || review == nil || review.Status != conformance.ReviewDone || len(verdicts) != 2 {
		t.Fatalf("review=%+v verdicts=%+v error=%v", review, verdicts, err)
	}
	for _, v := range verdicts {
		if v.SHA != sha {
			t.Fatalf("wrong source: %+v", v)
		}
		switch v.Check {
		case "script":
			if v.State != conformance.Conforms || v.Evaluator != "sh" {
				t.Fatalf("inferred shell was not executed: %+v", v)
			}
		case "rubric":
			if v.State != conformance.NotApplicable || v.Evaluator != "llm" {
				t.Fatalf("diff rubric was misclassified: %+v", v)
			}
		default:
			t.Fatalf("unexpected check: %+v", v)
		}
	}
}

// A what-if runs a pack version's checks on a project's main beside the
// checks it adopted, compared by block and check name, and is hypothetical
// by construction: the project's reviews and standing verdicts are what
// they were, and no verdict exists for a check it has not adopted. A failing
// check the version brings carries the brief a fix would start from; a diff
// rubric and a runtime probe with nothing serving main say why they were
// not judged. The project is not on the pack, so every row says it would
// join.
func TestAWhatIfComparesAVersionAndLeavesStandingAlone(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	tgLocalSh(t, a)
	ctx := testConformanceContext("boss")

	alpha, _, _ := fleetRepo(t, a, "alpha", map[string]string{"b.txt": "// FIXME later"})
	adopt(t, a, alpha, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	lib := wow.NewLibrary(a.DB)
	if _, err := lib.StockUpdate(ctx, wow.StockBlock{Name: "hygiene", Body: "body", ChecksJSON: checksJSON(shCheck("no-fixme", "exit 0", "high"))}); err != nil {
		t.Fatal(err)
	}
	if err := lib.StockInsert(ctx, wow.StockBlock{Name: "strict-docs", Body: "body", ChecksJSON: checksJSON(shCheck("has-readme", "test -f README.md", "high"), llmCheck("tone"),
		`{"name":"up","evaluator":"runtime","probe":"page","severity":"high","expects":"the page answers"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(ctx, "team", "Team", `{"guidance":["hygiene","strict-docs"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fleetReviewsFor(a).RunFleetReviewWith(ctx, "manual", conformance.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	reviewsBefore, err := a.conformance.Reviews(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	standing, err := savedLatestVerdict(a, alpha, "hygiene", 1, "no-fixme")
	if err != nil || standing.State != conformance.DoesNotConform {
		t.Fatalf("today's standing: %+v %v", standing, err)
	}

	id, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "team", Version: 1, Projects: []ids.ID{alpha}})
	if err != nil {
		t.Fatal(err)
	}
	got := runWhatIf(t, a, ctx, id)
	if got.State != "done" || got.ProjectsDone != 1 {
		t.Fatalf("the what-if finished: %+v", got)
	}
	rows := map[string]conformance.WhatIfRow{}
	for _, r := range got.Rows {
		rows[r.Block+"/"+r.Check] = r
	}
	if r := rows["hygiene/no-fixme"]; r.Result != conformance.WhatIfWouldPass || r.CurrentVersion != 1 || r.ProposedVersion != 2 {
		t.Errorf("a check the version relaxes: %+v", r)
	}
	if r := rows["strict-docs/has-readme"]; r.Result != conformance.WhatIfWouldFail || !strings.Contains(r.Brief, "strict-docs@1") || r.History != nil || r.Current != "" {
		t.Errorf("a check the version adds: %+v", r)
	}
	if r := rows["strict-docs/tone"]; r.Result != conformance.WhatIfNotAssessed || r.Reason != conformance.NotAssessedJudgesAChange || !strings.HasPrefix(r.Detail, r.Reason.Words()) {
		t.Errorf("a diff rubric says it judges a change: %+v", r)
	}
	if r := rows["strict-docs/up"]; r.Result != conformance.WhatIfNotAssessed || r.Reason != conformance.NotAssessedNoRunningApp {
		t.Errorf("a runtime probe with no deployment serving main: %+v", r)
	}
	for _, r := range got.Rows {
		if !r.Joins {
			t.Errorf("alpha is not on the pack; every row says it would join: %+v", r)
		}
	}
	outsider := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "outsider", Via: "token"})
	_, reviewsErr := a.conformance.Reviews(outsider, 10)
	if _, err := a.conformance.WhatIf(outsider, id); (err == nil) != (reviewsErr == nil) {
		t.Fatalf("a what-if is read by the rule that reads reviews: what-if %v, reviews %v", err, reviewsErr)
	}
	if latest, err := a.conformance.LatestWhatIf(ctx, "team", 1); err != nil || latest == nil || latest.ID != id {
		t.Fatalf("the version's newest what-if: %+v %v", latest, err)
	}
	if none, err := a.conformance.LatestWhatIf(ctx, "team", 2); err != nil || none != nil {
		t.Fatalf("a version no what-if ran on: %+v %v", none, err)
	}

	reviewsAfter, err := a.conformance.Reviews(ctx, 100)
	if err != nil || len(reviewsAfter) != len(reviewsBefore) {
		t.Fatalf("a what-if began a review: %d → %d %v", len(reviewsBefore), len(reviewsAfter), err)
	}
	if after, err := savedLatestVerdict(a, alpha, "hygiene", 1, "no-fixme"); err != nil || after.ID != standing.ID {
		t.Fatalf("a what-if moved standing: %+v %v", after, err)
	}
	if _, err := savedLatestVerdict(a, alpha, "strict-docs", 1, "has-readme"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a what-if wrote a verdict for a check the project has not adopted: %v", err)
	}
}

// Starting a what-if takes the authority of a fleet review and reading one
// takes conformance read, on the API and through MCP alike: a person
// without them is refused before anything is written or any project named
// (a start on the eligible projects included), an administrator's
// agent may read but the route that starts one is a person's or their
// command line's.
func TestWhatIfAuthority(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	if _, err := a.Ways.PublishPack(t.Context(), "team", "Team", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	start := `{"pack":"team","version":1,"projects":["game"]}`
	if w := authzCall(h, "outsider", "POST", "/api/v1/conformance/what-if", start); w.Code != 403 {
		t.Fatalf("an outsider started a what-if: %d %s", w.Code, w.Body.String())
	}
	// No project is on an earlier version than v1: refused for authority,
	// not for that, so the outsider learns nothing about who holds the pack.
	if w := authzCall(h, "outsider", "POST", "/api/v1/conformance/what-if", `{"pack":"team","version":1}`); w.Code != 403 {
		t.Fatalf("an outsider started a what-if on the eligible projects: %d %s", w.Code, w.Body.String())
	}
	if none, err := a.conformance.LatestWhatIf(testConformanceContext("boss"), "team", 1); err != nil || none != nil {
		t.Fatalf("a refused start wrote a what-if: %+v %v", none, err)
	}
	w := authzCall(h, "boss", "POST", "/api/v1/conformance/what-if", start)
	var started struct{ ID ids.ID }
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || w.Code != 202 || started.ID.IsZero() {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{fmt.Sprintf("/api/v1/conformance/what-if/%d", started.ID), "/api/v1/conformance/what-if?pack=team&version=1"} {
		if w := authzCall(h, "boss", "GET", path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"pack":"team"`) {
			t.Fatalf("read %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	reader := mcp_token(t, a, "boss-agent", "boss", "admin")
	if res := mcp_call(t, a, reader, "get_what_if", map[string]any{"pack": "team", "version": 1}); res["isError"] == true {
		t.Fatalf("an administrator's agent reads a what-if: %v", res)
	}
	if res := mcp_call(t, a, reader, "get_what_if", map[string]any{"id": idstest.N(999999).String()}); res["isError"] != true {
		t.Fatalf("a what-if that does not exist: %v", res)
	}
}

// A what-if that names no projects runs on every eligible one: the active
// projects on an earlier version of the pack, here alpha on team v1, not
// beta, which is not on the pack, and not gone, deleted and kept. The record keeps the list it ran on, and its
// read answers per project with the checks the version would newly fail.
// The version's read names the eligible projects; an empty list is refused,
// and so is a version no project is behind, with nothing admitted. Kills:
// the default left as "name the projects"; eligibility widened to projects
// not on the pack or to a deleted project; the selection not stored; the per-project answer
// missing from the read.
func TestAWhatIfWithNoProjectsNamedRunsOnTheEligibleOnes(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	tgLocalSh(t, a)
	ctx := testConformanceContext("boss")
	h := a.Handler()

	alpha, _, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "a"})
	fleetRepo(t, a, "beta", map[string]string{"b.txt": "b"})
	gone, _, _ := fleetRepo(t, a, "gone", map[string]string{"g.txt": "g"})
	if err := wow.NewLibrary(a.DB).StockInsert(ctx, wow.StockBlock{Name: "strict-docs", Body: "body", ChecksJSON: checksJSON(shCheck("has-readme", "test -f README.md", "high"))}); err != nil {
		t.Fatal(err)
	}
	for i, id := range []ids.ID{alpha, gone} {
		adopt(t, a, id, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
		if i == 0 {
			if _, err := a.Ways.PublishPack(ctx, "team", "Team", `{"guidance":["hygiene"]}`, ""); err != nil {
				t.Fatal(err)
			}
		}
		candidate, err := a.Adoption.Prepare(ctx, id, adoption.Request{Packs: []adoption.PackPick{{Pack: "team", Version: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.lifecycle.Adopt(adoption.WithSystemAuthority(context.Background(), id), a.projectByID(context.Background(), id), candidate, "fixture", "take team v1", adoption.OperationSelect); err != nil {
			t.Fatal(err)
		}
	}
	// A deleted project, kept until it is purged, holds team v1 and is not
	// eligible: it takes no new work.
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
		if err := project.MarkDeletingIn(ctx, tx, gone, access.Actor{Name: "boss"}, ids.Nil, false, time.Now()); err != nil {
			return err
		}
		return project.MarkDeletedIn(ctx, tx, gone, time.Now(), time.Now().Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(ctx, "team", "Team", `{"guidance":["hygiene","strict-docs"]}`, ""); err != nil {
		t.Fatal(err)
	}

	if w := authzCall(h, "boss", "POST", "/api/v1/conformance/what-if", `{"pack":"team","version":2,"projects":[]}`); w.Code != 400 {
		t.Fatalf("an empty list is not the eligible projects: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/conformance/what-if", `{"pack":"team","version":1}`); w.Code != 400 {
		t.Fatalf("no project is behind v1: %d %s", w.Code, w.Body.String())
	}
	if none, err := a.conformance.LatestWhatIf(ctx, "team", 1); err != nil || none != nil {
		t.Fatalf("a refused what-if was admitted: %+v %v", none, err)
	}
	var latest struct{ Eligible []string }
	if w := authzCall(h, "boss", "GET", "/api/v1/conformance/what-if?pack=team&version=2", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &latest) != nil || !slices.Equal(latest.Eligible, []string{"alpha"}) {
		t.Fatalf("the version's eligible projects: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "boss", "POST", "/api/v1/conformance/what-if", `{"pack":"team","version":2}`)
	var started struct{ ID ids.ID }
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || w.Code != 202 {
		t.Fatalf("start on the eligible projects: %d %s", w.Code, w.Body.String())
	}
	got := runWhatIf(t, a, ctx, started.ID)
	if got.State != conformance.WhatIfDone || got.ProjectsTotal != 1 || len(got.Projects) != 1 {
		t.Fatalf("ran on the eligible project only: %+v", got)
	}
	p := got.Projects[0]
	if p.ProjectID != alpha || p.Joins || p.Outcome != conformance.WhatIfNewFailures || len(p.NewFailures) != 1 || p.NewFailures[0].Check != "has-readme" {
		t.Fatalf("alpha's answer: %+v", p)
	}
}

// A what-if keeps the exception matcher's answer on the version's side, as
// adoption would: a whole-check exception names its block version, so it
// stops covering a block the version moves (excepted today, failing under
// the version: a new failure, marked excepted today), and still covers a
// block the version keeps (excepted both ways: nothing new). Kills: the
// version's side turning every exception into a failure; a lapse not
// counted as a new failure; a kept exception reported as one.
func TestAWhatIfSaysWhichExceptionsTheVersionLeavesUncovered(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	tgLocalSh(t, a)
	ctx := testConformanceContext("boss")

	alpha, _, _ := fleetRepo(t, a, "alpha", map[string]string{"b.txt": "// FIXME later"})
	fails := checksJSON(shCheck("no-fixme", noFixmeSh, "high"))
	adopt(t, a, alpha, "moved", 1, fails)
	adopt(t, a, alpha, "kept", 1, fails)
	for _, block := range []string{"moved", "kept"} {
		if _, err := a.conformance.CreateDisposition(ctx, conformance.ExceptionRequest{ProjectID: alpha, Fingerprint: "guidance|" + block + "@1|no-fixme|", Kind: conformance.AcceptedRisk, Justification: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := wow.NewLibrary(a.DB).StockUpdate(ctx, wow.StockBlock{Name: "moved", Body: "body v2", ChecksJSON: fails}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(ctx, "team", "Team", `{"guidance":["moved","kept"]}`, ""); err != nil {
		t.Fatal(err)
	}
	id, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "team", Version: 1, Projects: []ids.ID{alpha}})
	if err != nil {
		t.Fatal(err)
	}
	got := runWhatIf(t, a, ctx, id)
	rows := map[string]conformance.WhatIfRow{}
	for _, r := range got.Rows {
		rows[r.Block] = r
	}
	excepted, failing := conformance.Excepted.String(), conformance.DoesNotConform.String()
	if r := rows["moved"]; r.Current != excepted || r.Proposed != failing || r.ProposedVersion != 2 || !strings.HasPrefix(r.Detail, "excepted today") {
		t.Errorf("the moved block's exception no longer covers it: %+v", r)
	}
	if r := rows["kept"]; r.Current != excepted || r.Proposed != excepted || r.Result != conformance.WhatIfUnchanged {
		t.Errorf("the kept block's exception still covers it: %+v", r)
	}
	if len(got.Projects) != 1 {
		t.Fatalf("one project: %+v", got.Projects)
	}
	p := got.Projects[0]
	if p.Outcome != conformance.WhatIfNewFailures || len(p.NewFailures) != 1 || p.NewFailures[0].Block != "moved" || !p.NewFailures[0].ExceptedToday || len(p.NotAssessed) != 0 {
		t.Fatalf("alpha's answer: %+v", p)
	}
}

// Adopting from a what-if names the what-if, and the forge reads from it
// what was assessed for the project: pack, version, adoption revision and
// candidate. The Change opens with that candidate as its recorded target
// (the other pack and the project's own guidance kept, nothing moved until
// it lands) and its brief names the what-if, the commit and the project's
// actual outcome. Refused, with nothing opened: a what-if not done; a
// project it did not select; a project it selected but prepared no
// candidate for; a project whose adoption moved since; one whose candidate
// changed at the same revision; the what-if beside a selection or a
// revision of the caller's; someone who may not configure the project.
// Kills: the caller's values trusted over the record; the revision or the
// digest check dropped; the basis line missing or claiming success; the
// door's authority bypassed.
func TestAdoptingFromAWhatIfOpensWhatItAssessed(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	tgLocalSh(t, a)
	ctx := testConformanceContext("boss")
	h := a.Handler()

	alpha, alphaSHA, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "a"})
	fleetRepo(t, a, "beta", map[string]string{"b.txt": "b"})
	// gamma has no adoption yet: a what-if that names it prepares no
	// candidate for it.
	gamma, _, _ := fleetRepo(t, a, "gamma", map[string]string{"g.txt": "g"})
	adopt(t, a, alpha, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	if err := wow.NewLibrary(a.DB).StockInsert(ctx, wow.StockBlock{Name: "strict-docs", Body: "body", ChecksJSON: checksJSON(shCheck("has-readme", "test -f README.md", "high"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(ctx, "team", "Team", `{"guidance":["hygiene"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := wow.NewLibrary(a.DB).StockInsert(ctx, wow.StockBlock{Name: "own-notes", Body: "body", ChecksJSON: checksJSON(shCheck("notes", "exit 0", "low"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(ctx, "extra", "Extra", `{"guidance":["own-notes"]}`, ""); err != nil {
		t.Fatal(err)
	}
	candidate, err := a.Adoption.Prepare(ctx, alpha, adoption.Request{Packs: []adoption.PackPick{{Pack: "team", Version: 1}, {Pack: "extra", Version: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.lifecycle.Adopt(adoption.WithSystemAuthority(context.Background(), alpha), a.projectByID(context.Background(), alpha), candidate, "fixture", "take team v1", adoption.OperationSelect); err != nil {
		t.Fatal(err)
	}
	adopt(t, a, alpha, "loose", 1, checksJSON(shCheck("loose", "exit 0", "low")))
	if _, err := a.Ways.PublishPack(ctx, "team", "Team", `{"guidance":["hygiene","strict-docs"]}`, ""); err != nil {
		t.Fatal(err)
	}
	whatIf := func(projects ...ids.ID) *conformance.WhatIf {
		t.Helper()
		id, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "team", Version: 2, Projects: projects})
		if err != nil {
			t.Fatal(err)
		}
		return runWhatIf(t, a, ctx, id)
	}
	got := whatIf(alpha, gamma)
	var assessed conformance.WhatIfProject
	for _, p := range got.Projects {
		if p.ProjectID == alpha {
			assessed = p
		}
	}
	if assessed.CandidateDigest == "" || assessed.Outcome != conformance.WhatIfNewFailures {
		t.Fatalf("the what-if's answer for alpha: %+v", got.Projects)
	}
	adoptFrom := func(who, slug, body string) (int, string) {
		w := authzCall(h, who, "POST", "/api/v1/projects/"+slug+"/adoption/adopt", body)
		return w.Code, w.Body.String()
	}
	from := func(id ids.ID) string { return fmt.Sprintf(`{"fromWhatIf":%q,"viaChange":true}`, id) }
	queued, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "team", Version: 2, Projects: []ids.ID{alpha}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, who, slug, body, says string
		code                        int
	}{
		{"a what-if not done", "boss", "alpha", from(queued), "not done", 409},
		{"a project it did not select", "boss", "beta", from(got.ID), "assessed no candidate", 409},
		{"a project it prepared no candidate for", "boss", "gamma", from(got.ID), "assessed no candidate", 409},
		{"beside a selection", "boss", "alpha", fmt.Sprintf(`{"fromWhatIf":%q,"packs":[],"viaChange":true}`, got.ID), "name it alone", 400},
		{"beside additions", "boss", "alpha", fmt.Sprintf(`{"fromWhatIf":%q,"additions":["loose"],"viaChange":true}`, got.ID), "name it alone", 400},
		{"beside a revision", "boss", "alpha", fmt.Sprintf(`{"fromWhatIf":%q,"expectedRevision":%d,"viaChange":true}`, got.ID, assessed.AdoptionRevision), "name it alone", 400},
		{"someone who may not configure alpha", "outsider", "alpha", from(got.ID), "", 403},
		// A page loaded before fromWhatIf sends the older move/expectedDigest request:
		// refused, not read as adopting the current selection.
		{"the old page's request", "boss", "alpha", fmt.Sprintf(`{"move":{"pack":"team","version":2},"expectedRevision":%d,"expectedDigest":%q,"viaChange":true}`, assessed.AdoptionRevision, assessed.CandidateDigest), "", 400},
	} {
		if code, body := adoptFrom(c.who, c.slug, c.body); code != c.code || !strings.Contains(body, c.says) {
			t.Fatalf("%s: %d %s", c.name, code, body)
		}
	}
	// A candidate other than the one prepared now, at the same revision. No
	// operation makes that state (a pack version's contents and the
	// project's picks are fixed at a revision), so the record says so.
	// sql-guarantee: the digest check must refuse a record whose candidate differs at the current revision; no owner operation produces one.
	if _, err := a.DB.SQL().ExecContext(context.Background(), `UPDATE what_if_rows SET candidate_digest = 'not-this-one' WHERE what_if_id = $1 AND project_id = $2`, got.ID, alpha); err != nil {
		t.Fatal(err)
	}
	if code, body := adoptFrom("boss", "alpha", from(got.ID)); code != 409 || !strings.Contains(body, "since it was assessed") {
		t.Fatalf("a candidate other than the assessed one, at the same revision: %d %s", code, body)
	}
	// Assessed afresh, it opens.
	fresh := whatIf(alpha)
	assessed = fresh.Projects[0]
	code, body := adoptFrom("boss", "alpha", from(fresh.ID))
	var opened struct {
		ChangeID ids.ID `json:"changeId"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &opened) != nil || opened.ChangeID.IsZero() {
		t.Fatalf("adopting what was assessed: %d %s", code, body)
	}
	ch, err := a.changeByID(context.Background(), opened.ChangeID)
	if err != nil || ch.ExpectedRevision != assessed.AdoptionRevision {
		t.Fatalf("the Change: %+v %v", ch, err)
	}
	if want := fmt.Sprintf("Assessed in what-if #%d at %s: new failures.", fresh.ID, strs.ShortSHA(alphaSHA)); !strings.Contains(ch.Body, want) {
		t.Fatalf("the brief names the what-if, the commit and the outcome %q:\n%s", want, ch.Body)
	}
	target, err := a.Adoption.ByID(ctx, ch.TargetAdoptionID)
	if err != nil || target.Digest != assessed.CandidateDigest {
		t.Fatalf("the recorded target is not the assessed candidate: %+v %v", target, err)
	}
	held := map[string]bool{}
	for _, b := range target.Receipt.Guidance {
		held[b.Name] = true
	}
	if !held["loose"] || !held["own-notes"] || !held["strict-docs"] {
		t.Fatalf("the target keeps the project's own guidance and other pack and takes the version: %+v", target.Receipt.Guidance)
	}
	if now, err := a.Adoption.CurrentReceipt(ctx, alpha); err != nil || now.Revision != assessed.AdoptionRevision {
		t.Fatalf("opening the Change moved alpha's adoption: %+v %v", now, err)
	}
	// The project moves on: the what-if it was assessed in is no basis now.
	adopt(t, a, alpha, "more", 1, checksJSON(shCheck("more", "exit 0", "low")))
	if _, err := a.Work.Abandon(asPerson("boss"), workflow.StopWork{ChangeID: opened.ChangeID, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if code, body := adoptFrom("boss", "alpha", from(fresh.ID)); code != 409 || !strings.Contains(body, "prepare again") {
		t.Fatalf("a project whose adoption moved since: %d %s", code, body)
	}
}

// Recommending on a what-if names it on the recommendation's receipt; the
// forge checks it is a finished what-if of that pack and version. One of
// another version, of another pack at that version number, or not finished
// is refused and nothing moves. Kills: the what-if's number recorded
// unchecked; a refused recommendation moving the pointer; the receipt
// without the what-if.
func TestARecommendationNamesTheWhatIfItWasDecidedOn(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	tgLocalSh(t, a)
	ctx := testConformanceContext("boss")
	h := a.Handler()
	alpha, _, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "a"})
	for _, spec := range []string{`{}`, `{"toolchain":"go"}`} {
		if _, err := a.Ways.PublishPack(ctx, "team", "Team", spec, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Ways.PublishPack(ctx, "other", "Other", spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	run := func(pack string, version int64) ids.ID {
		id, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: pack, Version: version, Projects: []ids.ID{alpha}})
		if err != nil {
			t.Fatal(err)
		}
		return runWhatIf(t, a, ctx, id).ID
	}
	ofV1, ofOther := run("team", 1), run("other", 2)
	queued, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "team", Version: 2, Projects: []ids.ID{alpha}})
	if err != nil {
		t.Fatal(err)
	}
	recommend := func(whatIf ids.ID) (int, string) {
		w := authzCall(h, "boss", "POST", "/api/v1/packs/team/recommended", fmt.Sprintf(`{"version":2,"whatIf":%q}`, whatIf))
		return w.Code, w.Body.String()
	}
	for name, id := range map[string]ids.ID{"another version": ofV1, "another pack": ofOther, "not finished": queued} {
		if code, body := recommend(id); code != 409 {
			t.Fatalf("a what-if of %s: %d %s", name, code, body)
		}
	}
	if p, err := a.Ways.Pack(ctx, "team"); err != nil || p.Version != 1 {
		t.Fatalf("a refused recommendation moved the pointer: %+v %v", p, err)
	}
	if got := auditEntries(t, a.DB, "pack.recommended"); len(got) != 0 {
		t.Fatalf("a refused recommendation left a receipt: %+v", got)
	}
	ofV2 := run("team", 2)
	if code, body := recommend(ofV2); code != 200 {
		t.Fatalf("recommending on a finished what-if of the version: %d %s", code, body)
	}
	got := auditEntries(t, a.DB, "pack.recommended")
	if len(got) != 1 || !strings.Contains(string(got[0].Payload), fmt.Sprintf(`"whatIf":%q`, ofV2)) {
		t.Fatalf("the receipt names the what-if: %+v", got)
	}
	// Again, on another finished what-if of v2: v2 is already recommended,
	// so nothing moves, nothing is recorded, and the forge says so rather
	// than answer as if the second what-if were the basis.
	if code, body := recommend(run("team", 2)); code != 409 || !strings.Contains(body, "already recommended") {
		t.Fatalf("a second basis for a recommendation already made: %d %s", code, body)
	}
	if again := auditEntries(t, a.DB, "pack.recommended"); len(again) != 1 {
		t.Fatalf("a no-op recommendation left a receipt: %+v", again)
	}
}

// runWhatIf runs an admitted what-if's job, as the conformance queue's
// worker would, and reads what it recorded.
func runWhatIf(t *testing.T, a *App, ctx context.Context, id ids.ID) *conformance.WhatIf {
	t.Helper()
	runQueuedJobForTest(t, a, verification.WhatIfJob, fmt.Sprint(id))
	got, err := a.conformance.WhatIf(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A what-if sends model rubrics only when its starter included them with a
// spend limit, and asks the limit before each project's checklist: once
// observed spend reaches it, or any call's spend is unknown, the rest are
// not sent and say why. The overshoot is one project's checklist, which may
// be two paid calls when the first answer is not JSON. What it cost is on
// the what-if ("at least" when unknown), from the usage each call returns;
// on the usage ledger it is the gateway's one line per paid call, and no
// second line of the what-if's own. The calls go through the real gateway
// to a fake provider.
//
// Kills: rubrics sent without the starter including them; the limit read
// once at the start, or not at all; a retry's spend dropped; unknown spend
// treated as none; rubric spend missing from the ledger, or written twice.
func TestAWhatIfSpendsOnModelRubricsOnlyUnderItsLimit(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	tgLocalSh(t, a)
	ctx := testConformanceContext("boss")
	var projects []ids.ID
	for _, slug := range []string{"one", "two", "three"} {
		id, _, _ := fleetRepo(t, a, slug, map[string]string{"notes.txt": "kind words"})
		adopt(t, a, id, "base", 1, checksJSON(shCheck("ok", "exit 0", "low")))
		projects = append(projects, id)
	}
	if err := wow.NewLibrary(a.DB).StockInsert(ctx, wow.StockBlock{Name: "judged", Body: "body", ChecksJSON: checksJSON(treeRubric("tone", "be kind", "*.txt"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(ctx, "judged-pack", "Judged", `{"guidance":["judged"]}`, ""); err != nil {
		t.Fatal(err)
	}
	var answers []string
	calls := 0
	setFastSlot(t, a, fakeModelProvider(t, a.Integrations.Bindings, func(providerQuestion) (int, string) {
		body := answers[min(calls, len(answers)-1)]
		calls++
		return 200, body
	}))
	run := func(in conformance.WhatIfRequest) map[conformance.NotAssessed]int {
		t.Helper()
		calls = 0
		in.Pack, in.Version, in.Projects = "judged-pack", 1, projects
		id, err := fleetReviewsFor(a).StartWhatIf(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		got := runWhatIf(t, a, ctx, id)
		if got.State != conformance.WhatIfDone {
			t.Fatalf("the what-if finished: %+v", got)
		}
		reasons := map[conformance.NotAssessed]int{}
		for _, r := range got.Rows {
			if r.Check == "tone" {
				reasons[r.Reason]++
			}
		}
		return reasons
	}
	pass := `[{"item":1,"verdict":"pass","note":"kind"}]`

	if _, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "judged-pack", Version: 1, Projects: projects, Rubrics: true}); err == nil {
		t.Fatal("rubrics without a spend limit were admitted")
	}
	answers = []string{chatAnswer(pass, 1, 1, 1)}
	if got := run(conformance.WhatIfRequest{}); calls != 0 || got[conformance.NotAssessedRubricsNotIncluded] != 3 {
		t.Fatalf("rubrics not included: %d calls, reasons %v", calls, got)
	}

	answers = []string{chatAnswer("prose, not JSON", 4, 2, 0.4), chatAnswer(pass, 4, 2, 0.4)}
	got := run(conformance.WhatIfRequest{Rubrics: true, SpendLimitMicros: 500_000})
	if calls != 2 || got[""] != 1 || got[conformance.NotAssessedSpendLimit] != 2 {
		t.Fatalf("one checklist (asked twice) spends past the limit, the rest are not sent: %d calls, reasons %v", calls, got)
	}
	latest, err := a.conformance.LatestWhatIf(ctx, "judged-pack", 1)
	if err != nil || latest.CostMicros != 800_000 || latest.Incomplete != 0 {
		t.Fatalf("the retried checklist's whole spend is on the what-if: %+v %v", latest, err)
	}
	report, err := a.Usage.Ledger.Summary(ctx, 1, ids.Nil, "")
	if err != nil {
		t.Fatal(err)
	}
	ledgered := int64(0)
	for _, l := range report.ByKind {
		if l.Key == "what-if" {
			ledgered = l.CostMicros
		}
	}
	if ledgered != 800_000 {
		t.Fatalf("the usage ledger's what-if lines: %d micros in %+v", ledgered, report.ByKind)
	}

	answers = []string{`{"choices":[{"message":{"content":"` + strings.ReplaceAll(pass, `"`, `\"`) + `"}}]}`}
	got = run(conformance.WhatIfRequest{Rubrics: true, SpendLimitMicros: 10_000_000})
	if calls != 1 || got[""] != 1 || got[conformance.NotAssessedSpendUnknown] != 2 {
		t.Fatalf("unknown spend stops further checklists: %d calls, reasons %v", calls, got)
	}
	if latest, err := a.conformance.LatestWhatIf(ctx, "judged-pack", 1); err != nil || latest.Incomplete != 1 {
		t.Fatalf("unknown spend is counted, so the cost reads at least: %+v %v", latest, err)
	}
}

// An eval's comparison judges the checks both arms declare on the arm's
// landed main and on its Change's base, finding by finding: a check that
// lists its occurrences shows the one the arm added; a boolean check only
// one version declares still counts, and failing on both it is not
// comparable; a runtime probe with no deployment serving the arm's main,
// and a rubric with no judge, say why they were not compared.
//
// Kills: the union narrowed to one arm's checks; a check failing on both
// trees read as unchanged; a check not answered read as fixed.
func TestAnEvalComparisonJudgesBothArmsChecksOnTheArmAndItsBase(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	tgLocalSh(t, a)
	id, base, git := fleetRepo(t, a, "copy", map[string]string{"a.txt": "FIXME old"})
	if err := os.WriteFile(filepath.Join(a.projectRepoPath("copy"), "b.txt"), []byte("FIXME new"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "the arm's work")
	branch := strings.TrimSpace(git("rev-parse", "--abbrev-ref", "HEAD"))
	lib := wow.NewLibrary(a.DB)
	listing := `{"name":"no-fixme","evaluator":"sh","sh":"if grep -rl FIXME . ; then exit 1; fi; exit 0","occurrences":"lines","severity":"high","expects":"no FIXME"}`
	if err := lib.StockInsert(t.Context(), wow.StockBlock{Name: "hygiene", Body: "body", ChecksJSON: checksJSON(listing)}); err != nil {
		t.Fatal(err)
	}
	if err := lib.StockInsert(t.Context(), wow.StockBlock{Name: "strict", Body: "body", ChecksJSON: checksJSON(shCheck("has-readme", "test -f README.md", "high"),
		`{"name":"up","evaluator":"runtime","probe":"page","severity":"high","expects":"the page answers"}`, treeRubric("tone", "be kind", "*.txt"))}); err != nil {
		t.Fatal(err)
	}
	got, err := fleetReviewsFor(a).CompareArm(context.Background(), verification.ArmComparison{EvalID: idstest.N(1), ProjectID: id, Slug: "copy", Branch: branch, BaseSHA: base,
		Blocks: []conformance.CheckVersion{{Block: "hygiene", Version: 1}, {Block: "strict", Version: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]verification.CheckRow{}
	for _, r := range got.Rows {
		rows[r.Check] = r
	}
	if r := rows["no-fixme"]; !slices.Equal(r.New, []string{"./b.txt"}) || !slices.Equal(r.AlreadyThere, []string{"./a.txt"}) || r.NotComparable != "" {
		t.Errorf("an occurrence the arm added: %+v", r)
	}
	if r := rows["has-readme"]; r.NotComparable == "" || r.New != nil {
		t.Errorf("a boolean check failing on both: %+v", r)
	}
	if r := rows["up"]; !strings.HasPrefix(r.NotComparable, "on this arm: needs the app running") {
		t.Errorf("a probe with no deployment serving the arm: %+v", r)
	}
	if r := rows["tone"]; !strings.Contains(r.NotComparable, "no judge model") {
		t.Errorf("a rubric with no judge: %+v", r)
	}
	if len(got.Base) != 4 || got.Head == base {
		t.Fatalf("the base was read, and the arm at its own commit: %+v", got)
	}
}

// An eval's scorecard is read by an agent through its person's read of
// evals (standards:read-evals, a read, so an agent token may use it; the
// page once asked standards:manage-packs, an edit, which no agent token
// reaches).
func TestEvalReadsTakeTheScorecardsAuthority(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	admin := mcp_token(t, a, "boss-agent", "boss", "admin")
	if res := mcp_call(t, a, admin, "get_eval", map[string]any{"pack": "team", "version": 1}); res["isError"] == true {
		t.Fatalf("an administrator's agent reads evals: %v", res)
	}
	if res := mcp_call(t, a, admin, "get_eval", map[string]any{"id": 99}); res["isError"] != true {
		t.Fatalf("an eval that does not exist: %v", res)
	}
}

// A fleet review skips a project that is being deleted: it takes no new
// work, so it is not assessed, and no verdict is recorded for it.
//
// Kills: a fleet review assessing (and remediating from) a project being
// deleted.
func TestFleetReviewSkipsAProjectBeingDeleted(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	testAdministrator(t, a, "tester")
	tgLocalSh(t, a)
	alpha, _, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "all good"})
	adopt(t, a, alpha, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	beta, _, _ := fleetRepo(t, a, "beta", map[string]string{"b.txt": "// FIXME hurry"})
	adopt(t, a, beta, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	if _, err := a.deletion.Request(asPerson("tester"), beta); err != nil {
		t.Fatal(err)
	}
	_, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("tester"), "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range verdicts {
		if v.ProjectSlug == "beta" {
			t.Fatalf("a project being deleted was reviewed: %+v", v)
		}
	}
	if len(verdicts) == 0 {
		t.Fatal("the active project was not reviewed")
	}
}

// A what-if whose job is stopped while it evaluates (the forge shutting
// down, or the queue's two-hour limit) ends interrupted, keeping the
// projects it recorded; it is not left running and not resumed. The stop
// arrives during the second project's model call.
//
// Kills: a stopped what-if recorded as an error, or left running until
// the next restart; its finished projects' rows lost.
func TestAWhatIfStoppedWhileItRunsEndsInterrupted(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	tgLocalSh(t, a)
	ctx := testConformanceContext("boss")
	var projects []ids.ID
	for _, slug := range []string{"one", "two"} {
		id, _, _ := fleetRepo(t, a, slug, map[string]string{"notes.txt": "kind words"})
		adopt(t, a, id, "base", 1, checksJSON(shCheck("ok", "exit 0", "low")))
		projects = append(projects, id)
	}
	if err := wow.NewLibrary(a.DB).StockInsert(ctx, wow.StockBlock{Name: "judged", Body: "body", ChecksJSON: checksJSON(treeRubric("tone", "be kind", "*.txt"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(ctx, "judged-pack", "Judged", `{"guidance":["judged"]}`, ""); err != nil {
		t.Fatal(err)
	}
	job, stop := context.WithCancel(context.Background())
	defer stop()
	calls := 0
	setFastSlot(t, a, fakeModelProvider(t, a.Integrations.Bindings, func(providerQuestion) (int, string) {
		if calls++; calls == 2 {
			stop()
		}
		return 200, chatAnswer(`[{"item":1,"verdict":"pass","note":"kind"}]`, 1, 1, 0.01)
	}))
	id, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "judged-pack", Version: 1, Projects: projects, Rubrics: true, SpendLimitMicros: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fleetReviewsFor(a).RunWhatIfJob(job, fmt.Sprint(id), nil); err != nil {
		t.Fatal(err)
	}
	got, err := a.conformance.WhatIf(ctx, id)
	if err != nil || got.State != conformance.WhatIfInterrupted || got.ProjectsDone != 1 || len(got.Rows) == 0 || !got.FinishedAt.Valid {
		t.Fatalf("a what-if stopped in its second project: %+v %v", got, err)
	}
	for _, r := range got.Rows {
		if r.ProjectID != projects[0] {
			t.Fatalf("a row of the project it did not finish: %+v", r)
		}
	}
}
