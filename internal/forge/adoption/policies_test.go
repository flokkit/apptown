package adoption

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/wow"
)

type policyCountedQueries struct {
	database.Handle
	reads int
}

func (d *policyCountedQueries) SQL() database.Handle { return d.Handle.WithExecutor(d) }

func (d *policyCountedQueries) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.DBTX.ExecContext(ctx, q, args...)
}

func (d *policyCountedQueries) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	d.reads++
	return d.DBTX.QueryContext(ctx, q, args...)
}

func (d *policyCountedQueries) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.DBTX.QueryRowContext(ctx, q, args...)
}

func policyProject(t *testing.T, db *database.DB, slug string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func policyExec(t *testing.T, db *database.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPolicyRules(t *testing.T) {
	t.Parallel()
	bad := []Policy{
		{Name: "a", Rule: "requireStepBefore", With: map[string]any{"uses": "deploy"}},
		{Name: "b", Rule: "requireCapabilityBound", With: map[string]any{"capability": " scanner"}},
		{Name: "c", Rule: "scanFailOnCeiling", With: map[string]any{"max": "severe"}},
		{Name: "d", Rule: "requireTests"},
	}
	for _, p := range bad {
		if _, err := CheckPolicy(&p); !errors.Is(err, wow.ErrPolicyDeclaration) {
			t.Fatalf("%s accepted: %v", p.Name, err)
		}
	}
	req, err := CheckPolicy(&Policy{Name: "ok", Rule: "scanFailOnCeiling", With: map[string]any{"max": "high", "extra": 1}})
	if err != nil || req.Max != "high" {
		t.Fatalf("extra options are kept but not judged: %+v %v", req, err)
	}
	if wow.FailOnLooseness("") >= wow.FailOnLooseness("low") || wow.FailOnLooseness("critical") <= wow.FailOnLooseness("high") {
		t.Fatal("ceilings order from strictest to loosest")
	}
}

func TestPoliciesInstallAndLibraryWithReceipts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ps := newPolicies(db)
	catalog := wow.NewPolicies(db)
	pid := adoptionTestProject(t, db, "pol", "Pol")
	if err := ps.Install(t.Context(), pid, "bad", "", "requireTests", nil, "", "alice"); !errors.Is(err, wow.ErrPolicyDeclaration) {
		t.Fatalf("a malformed policy was installed: %v", err)
	}
	if err := ps.Install(t.Context(), pid, "scan-first", "scan before deploy", "requireStepBefore", map[string]any{"uses": "deploy", "requires": "scan"}, "", "alice"); err != nil {
		t.Fatal(err)
	}
	if n := countOf(policiesOf(t, db, pid), func(p Policy) bool { return p.Name == "scan-first" }); n != 1 {
		t.Fatalf("installed policies: %d", n)
	}
	if n := receiptsWhere(t, db, pid, "policy.installed", func(e audit.Entry) bool { return e.Actor == "alice" }); n != 1 {
		t.Fatalf("install receipts: %d", n)
	}
	if err := ps.Remove(t.Context(), idstest.N(1<<40), "alice"); !errors.Is(err, ErrNoProjectPolicy) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removing an unknown project policy: %v", err)
	}
	// A receipt that cannot be written rolls the install back with it.
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if err := ps.Install(t.Context(), pid, "second", "", "requireCapabilityBound", map[string]any{"capability": "scanner"}, "", "alice"); err == nil {
		t.Fatal("an install without its receipt was reported as done")
	}
	if n := countOf(policiesOf(t, db, pid), func(p Policy) bool { return p.Name == "second" }); n != 0 { // pid is the only project
		t.Fatalf("install without receipt kept: %d", n)
	}

	if err := catalog.StockInsert(t.Context(), wow.LibraryPolicy{Name: "stock-one", Rule: "requireCapabilityBound", With: map[string]any{"capability": "scanner"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLibrary(t.Context(), wow.LibraryPolicy{Name: "stock-one", Rule: "requireCapabilityBound", With: map[string]any{"capability": "sbom"}}, "bob"); !errors.Is(err, wow.ErrStockPolicy) {
		t.Fatalf("a stock definition was overwritten: %v", err)
	}
	if err := catalog.DeleteLibrary(t.Context(), "stock-one", "bob"); !errors.Is(err, wow.ErrStockPolicy) {
		t.Fatalf("a stock definition was deleted: %v", err)
	}
	if err := catalog.SaveLibrary(t.Context(), wow.LibraryPolicy{Name: "ours", Rule: "scanFailOnCeiling", With: map[string]any{"max": "medium"}}, "bob"); err != nil {
		t.Fatal(err)
	}
	if lp := catalog.LibraryByName(t.Context(), "ours"); lp == nil || lp.Origin != "" || lp.With["max"] != "medium" {
		t.Fatalf("custom definition: %+v", lp)
	}
	if err := catalog.DeleteLibrary(t.Context(), "ours", "bob"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DeleteLibrary(t.Context(), "ours", "bob"); !errors.Is(err, wow.ErrNoLibraryPolicy) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting a missing definition: %v", err)
	}
	if n := countOf(ledger(t, db, ids.Nil, "policy-library."), func(e audit.Entry) bool { return e.Actor == "bob" }); n != 2 {
		t.Fatalf("library receipts: %d", n)
	}
}

func TestPolicyReadsPreserveOpaqueExactNumbers(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := policyProject(t, db, "policy-number")
	// A stored definition may carry options its rule does not read (one
	// saved before options were checked); its values come back exactly. It
	// is written as adoption copies a definition, which takes it as stored.
	options := map[string]any{"uses": "deploy", "requires": "scan", "identifier": int64(9007199254740993)}
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		return InstallPolicyIn(t.Context(), tx, pid, "exact", "", "requireStepBefore", options, "", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	policies, err := ReadProjectPolicies(context.Background(), db, pid)
	if err != nil || len(policies) != 1 {
		t.Fatalf("policies: %+v %v", policies, err)
	}
	raw, err := json.Marshal(policies[0].With)
	if err != nil || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("opaque number changed: %s %v", raw, err)
	}
}

func TestPolicyInstallationValidatesBeforeWritingAndChecksSuppression(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := policyProject(t, db, "policy-install")
	ps := newPolicies(db)
	if err := ps.Install(t.Context(), pid, "invalid", "", "requireStepBefore", map[string]any{"step": "scan", "before": "deploy"}, "", "apptown-system"); !errors.Is(err, wow.ErrPolicyDeclaration) {
		t.Fatalf("no-op policy installed: %v", err)
	}
	if p := policiesOf(t, db, pid); len(p) != 0 { // pid is the only project
		t.Fatalf("no-op policy installed: %+v", p)
	}
	options := map[string]any{"uses": "deploy", "requires": "scan"}
	if err := ps.Install(t.Context(), pid, "valid", "", "requireStepBefore", options, "", "apptown-system"); err != nil {
		t.Fatal(err)
	}
	policyExec(t, db, `CREATE FUNCTION veto_policy_install() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	policyExec(t, db, `CREATE TRIGGER veto_policy_install BEFORE INSERT OR UPDATE ON policies FOR EACH ROW EXECUTE FUNCTION veto_policy_install()`)
	if err := ps.Install(t.Context(), pid, "valid", "new words", "requireStepBefore", options, "", "apptown-system"); err == nil {
		t.Fatal("suppressed policy update returned success")
	}
	if err := ps.Install(t.Context(), pid, "second", "", "requireStepBefore", options, "", "apptown-system"); err == nil {
		t.Fatal("suppressed policy insert returned success")
	}
	if p := policiesOf(t, db, pid); len(p) != 1 || p[0].Name != "valid" || p[0].Description != "" {
		t.Fatalf("suppressed writes changed policies: %+v", p)
	}
}

func TestPolicyDeclarationReadErrorsDoNotBecomeEmptyViolations(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := policyProject(t, db, "policy-read-error")
	if err := newPolicies(db).Install(t.Context(), pid, "scan-first", "", "requireStepBefore", map[string]any{"uses": "deploy", "requires": "scan"}, "", "alice"); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "FROM policies")
	violations, err := New(db, Dependencies{}).CheckWorkflow(context.Background(), db, pid, "landing", &schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "ship", Steps: []schema.Step{{Name: "deploy", Uses: "deploy"}}}}})
	if violations != nil || !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("unreadable policies became conformance: %+v %v", violations, err)
	}
}

func TestPolicyReportBatchesWorkflowSelectionAndCapabilityFacts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := policyProject(t, db, "policy-report")
	s := New(db, Dependencies{Ways: wow.New(db, wow.Dependencies{})})
	if err := newPolicies(db).Install(t.Context(), pid, "scan-first", "", "requireStepBefore", map[string]any{"uses": "deploy", "requires": "scan"}, "", "alice"); err != nil {
		t.Fatal(err)
	}
	wf := wow.NewWorkflows(db)
	for i := range 20 {
		name := fmt.Sprintf("workflow-%02d", i)
		system := i == 19
		source := fmt.Sprintf("apiVersion: forge.flokkit.io/v1\nkind: Workflow\nmetadata: {name: %s, system: %t}\nspec:\n  kind: pipeline\n  stages:\n    - name: ship\n      steps: [{name: deploy, uses: deploy}]\n", name, system)
		if _, _, err := wf.Put(t.Context(), source, "alice"); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if err := s.SelectLane(t.Context(), pid, name, true, "fixture"); err != nil {
				t.Fatal(err)
			}
		}
	}
	counted := &policyCountedQueries{Handle: db.SQL()}
	violations, err := s.ProjectConformance(context.Background(), counted, pid)
	if err != nil || len(violations) != 11 || counted.reads != 3 {
		t.Fatalf("selection: violations=%d reads=%d %v", len(violations), counted.reads, err)
	}
	if err := newPolicies(db).Install(t.Context(), pid, "scan-first", "", "requireCapabilityBound", map[string]any{"capability": "scanner"}, "", "alice"); err != nil {
		t.Fatal(err)
	}
	counted = &policyCountedQueries{Handle: db.SQL()}
	violations, err = s.ProjectConformance(context.Background(), counted, pid)
	if err != nil || len(violations) != 11 || counted.reads != 4 {
		t.Fatalf("binding matrix: violations=%d reads=%d %v", len(violations), counted.reads, err)
	}
	dbtest.FailNextStatement(t, db, "FROM lib_workflows")
	if got, err := s.ProjectConformance(context.Background(), db, pid); got != nil || err == nil {
		t.Fatalf("unreadable catalog became conformance: %+v %v", got, err)
	}
	// sql-guarantee: point at an absent workflow version; PublishWorkflow would create that version and remove the read failure.
	policyExec(t, db, `UPDATE lib_workflows SET latest_version=2 WHERE name='workflow-00'`)
	if got, err := s.ProjectConformance(context.Background(), db, pid); got != nil || err == nil {
		t.Fatalf("missing selected definition became conformance: %+v %v", got, err)
	}
}

// The service's policy doors observe integration bindings the same way the
// package doors do: a project holding the stock "capability bound" policy
// is evaluated, not refused. (Service doors without an observer once
// made every run start on such a project fail.)
func TestServicePolicyDoorsObserveCapabilities(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	s := New(db, Dependencies{})
	policies := []Policy{{Name: "scanner-bound", Rule: "requireCapabilityBound", With: map[string]any{"capability": "scanner"}}}
	evaluations, err := PrepareEvaluations(ctx, db, policies)
	if err != nil || len(evaluations) != 1 || evaluations[0].Bound {
		t.Fatalf("service evaluations: %+v %v", evaluations, err)
	}
	pid := adoptionTestProject(t, db, "bound", "Bound")
	if err := newPolicies(db).Install(ctx, pid, "scanner-bound", "", "requireCapabilityBound", map[string]any{"capability": "scanner"}, "", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProjectConformance(ctx, db, pid); err != nil {
		t.Fatalf("service conformance: %v", err)
	}
	if _, err := s.CheckWorkflow(ctx, db, pid, "quick-change", &schema.Spec{}); err != nil {
		t.Fatalf("service workflow check: %v", err)
	}
}

func TestEvaluatePolicyRules(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	evaluate := func(policy *Policy, spec *schema.Spec) (string, error) {
		evaluations, err := PrepareEvaluations(context.Background(), db, []Policy{*policy})
		if err != nil {
			return "", err
		}
		return evaluations[0].Detail(spec), nil
	}
	specOf := func(y string) *schema.Spec {
		t.Helper()
		d, err := schema.ParseWorkflow([]byte(y))
		if err != nil {
			t.Fatal(err)
		}
		return &d.Spec
	}
	deployAfterScan := specOf(`
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: ok}
spec:
  kind: pipeline
  stages:
    - name: gates
      steps: [{name: s, uses: scan}]
    - name: ship
      steps: [{name: d, uses: deploy}]
`)
	deployNoScan := specOf(`
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: bad}
spec:
  kind: pipeline
  stages:
    - name: ship
      steps: [{name: d, uses: deploy}]
`)
	pol := &Policy{Rule: "requireStepBefore", With: map[string]any{"uses": "deploy", "requires": "scan"}}
	if v, err := evaluate(pol, deployAfterScan); err != nil || v != "" {
		t.Fatalf("conforming workflow flagged: %s", v)
	}
	if v, err := evaluate(pol, deployNoScan); err != nil || v == "" {
		t.Fatal("deploy without scan must violate")
	}
	// Rule not applicable (no deploy step at all) — silence, not violation.
	noDeploy := specOf(`
apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: quiet}
spec:
  kind: pipeline
  stages: [{name: only, steps: [{name: s, uses: scan}]}]
`)
	if v, err := evaluate(pol, noDeploy); err != nil || v != "" {
		t.Fatalf("inapplicable rule must be silent: %s", v)
	}
	// Unbound capability violates requireCapabilityBound on a fresh instance.
	capPol := &Policy{Rule: "requireCapabilityBound", With: map[string]any{"capability": "scanner"}}
	if v, err := evaluate(capPol, deployAfterScan); err != nil || v == "" {
		t.Fatal("fresh instance has no scanner binding; must violate")
	}
}
