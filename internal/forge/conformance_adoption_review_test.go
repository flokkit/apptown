package forge

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// adoptionReviewsOf is the adoption-triggered reviews of a project, newest
// first, as the Reviews page lists them.
func adoptionReviewsOf(t *testing.T, a *App) []conformance.ReviewSummary {
	t.Helper()
	all, err := a.conformance.Reviews(testConformanceContext("t"), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []conformance.ReviewSummary
	for _, r := range all {
		if r.Trigger == verification.AdoptionTrigger {
			out = append(out, r)
		}
	}
	return out
}

// Changing a project's way of working queues a review of that project,
// with the system as its actor (the fixture adopts with no person's review
// grant), one project and the one-project build allowance, and the job that
// runs it; the job judges the newly adopted check.
func TestAnAdoptionChangeQueuesItsProjectsReview(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "t")
	tgLocalSh(t, a)
	alpha, _, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "// FIXME"})
	adopt(t, a, alpha, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	queued := adoptionReviewsOf(t, a)
	if len(queued) != 1 || queued[0].Status != conformance.ReviewQueued || queued[0].Actor != "apptown-system" || queued[0].ProjectsTotal > 1 {
		t.Fatalf("the adoption's review: %+v", queued)
	}
	jobsFor := map[string]bool{}
	for _, j := range operationJobs(t, a, verification.ReviewJob) {
		jobsFor[j.Key] = true
	}
	if !jobsFor[queued[0].ID.String()] {
		t.Fatalf("no job runs review %d: %+v", queued[0].ID, jobsFor)
	}
	execution, err := a.conformance.ClaimReview(context.Background(), queued[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := execution.Options(); len(got.Projects) != 1 || got.Projects[0] != alpha || !got.Build || got.BuildBudget != 1 {
		t.Fatalf("the adoption's review asked: %+v", got)
	}

	// A second change queues a second review; its job judges the check.
	adopt(t, a, alpha, "hygiene", 2, checksJSON(shCheck("no-fixme", noFixmeSh, "high"), shCheck("no-todo", `exit 0`, "note")))
	queued = adoptionReviewsOf(t, a)
	if len(queued) != 2 || queued[0].Status != conformance.ReviewQueued {
		t.Fatalf("the second change's review: %+v", queued)
	}
	if _, err := fleetReviewsFor(a).RunReviewJob(context.Background(), queued[0].ID.String(), nil); err != nil {
		t.Fatal(err)
	}
	detail, err := a.conformance.Review(testConformanceContext("t"), queued[0].ID)
	if err != nil || detail.Review.Status != conformance.ReviewDone {
		t.Fatalf("the adoption's review ran: %+v %v", detail, err)
	}
	judged := map[string]conformance.Outcome{}
	for _, v := range detail.Verdicts {
		if v.ProjectID != alpha {
			t.Fatalf("the review judged another project: %+v", v)
		}
		judged[v.Check] = v.State
	}
	if judged["no-fixme"] != conformance.DoesNotConform || judged["no-todo"] != conformance.Conforms {
		t.Fatalf("the adoption's review found %+v", judged)
	}
}

// fixedAdoption is an adoption scope as Conformance sees one: the owner's
// value is exercised above; here each door's refusal is the guarantee.
type fixedAdoption struct {
	project ids.ID
	current bool
}

func (f fixedAdoption) ProjectID() ids.ID                     { return f.project }
func (f fixedAdoption) Current(context.Context) (bool, error) { return f.current, nil }

// The adoption scope admits a standing review of its own project while its
// revision is current, and nothing else: not another project, the fleet,
// a what-if, reads, exceptions, confirmations or history maintenance.
func TestTheAdoptionScopeAdmitsOnlyItsProjectsReview(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "t")
	alpha, _, _ := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "ok"})
	beta, _, _ := fleetRepo(t, a, "beta", map[string]string{"b.txt": "ok"})
	admit := func(ctx context.Context, in conformance.ReviewRequest) error {
		return a.DB.InTx(ctx, func(tx *database.Tx) error {
			_, err := a.conformance.AdmitReviewIn(ctx, tx, in)
			return err
		})
	}
	own := conformance.WithAdoption(context.Background(), fixedAdoption{project: alpha, current: true})
	if err := admit(own, conformance.ReviewRequest{Trigger: "adoption", Projects: []ids.ID{alpha}}); err != nil {
		t.Fatalf("its own project's review: %v", err)
	}
	for name, in := range map[string]conformance.ReviewRequest{
		"another project": {Trigger: "adoption", Projects: []ids.ID{beta}},
		"two projects":    {Trigger: "adoption", Projects: []ids.ID{alpha, beta}},
		"the fleet":       {Trigger: "adoption"},
		"no projects":     {Trigger: "adoption", Projects: []ids.ID{}},
	} {
		if err := admit(own, in); err == nil {
			t.Fatalf("%s was admitted", name)
		}
	}
	for name, scope := range map[string]fixedAdoption{"a revision no longer current": {project: alpha}, "no project": {current: true}} {
		ctx := conformance.WithAdoption(context.Background(), scope)
		if err := admit(ctx, conformance.ReviewRequest{Trigger: "adoption", Projects: []ids.ID{scope.project}}); err == nil {
			t.Fatalf("%s admitted a review", name)
		}
	}
	if err := a.DB.InTx(own, func(tx *database.Tx) error {
		_, err := a.conformance.AdmitWhatIfIn(own, tx, conformance.WhatIfRequest{Pack: "any", Version: 1, Projects: []ids.ID{alpha}}, nil)
		return err
	}); err == nil {
		t.Fatal("the adoption scope admitted a what-if")
	}
	if _, err := a.conformance.Dispositions(own, alpha, true); err == nil {
		t.Fatal("the adoption scope read its project's exceptions")
	}
	if _, err := a.conformance.Reviews(own, 10); err == nil {
		t.Fatal("the adoption scope read the estate")
	}
	if _, err := a.conformance.CreateDisposition(own, conformance.ExceptionRequest{ProjectID: alpha, Identifier: "x", Kind: conformance.AcceptedRisk, Justification: "no"}); err == nil {
		t.Fatal("the adoption scope made an exception")
	}
	if _, err := a.conformance.Confirm(own, conformance.Confirmation{VerdictID: idstest.N(1)}); err == nil {
		t.Fatal("the adoption scope confirmed a flag")
	}
	if err := a.DB.InTx(own, func(tx *database.Tx) error {
		_, err := a.conformance.PrepareHistoryClearIn(own, tx)
		return err
	}); err == nil {
		t.Fatal("the adoption scope cleared history")
	}
}
