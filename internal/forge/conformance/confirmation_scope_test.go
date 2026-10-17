package conformance

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type confirmationSource struct {
	testCheckSource
	head string
}

func (s *confirmationSource) Head(context.Context, ProjectSource) (SourceHead, error) {
	return SourceHead{SHA: s.head}, nil
}

func confirmationVerdict(t *testing.T, service *Service, projectID ids.ID, state Outcome) Verdict {
	t.Helper()
	ctx := caller("reviewer")
	execution, err := service.BeginReview(ctx, ReviewRequest{Trigger: "manual", Projects: []ids.ID{projectID}})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := execution.Projects(ctx)
	if err != nil || len(projects) != 1 || len(projects[0].Checks) != 1 {
		t.Fatalf("review project: %+v %v", projects, err)
	}
	check := projects[0].Checks[0]
	pending, err := execution.PrepareVerdict(ctx, Assessment{ProjectID: projectID,
		Check: CheckIdentity{Block: check.Block, Version: check.Version, Check: check.Check.Name}, State: state, Detail: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := pending.Commit(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Progress(ctx, 1, projects[0].Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Finish(ctx, Completion{Projects: 1, ProjectsDone: 1}, ReviewDone); err != nil {
		t.Fatal(err)
	}
	return verdict
}

func confirmationStanding(t *testing.T, view *View, slug string) ProjectStanding {
	t.Helper()
	for _, standing := range view.Projects {
		if standing.Slug == slug {
			return standing
		}
	}
	t.Fatalf("no standing for %s: %+v", slug, view.Projects)
	return ProjectStanding{}
}

// A confirm is an adjudication of one immutable verdict. A later review can
// display prior confirmation, but cannot inherit its decision.
func TestConfirmIsPerVerdict(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := createTestProject(t, db, "proj", "Proj", "main")
	block := stockCheckFixture(t, db, wow.StockBlock{Name: "prose", ChecksJSON: `[{"name":"readme-plain","evaluator":"llm","scope":"tree","rubric":"README is plain","files":["README.md"]}]`})
	pinCheckFixture(t, db, pid, wow.ResolvedPin{BlockID: block, Name: "prose", Version: 1})
	source := &confirmationSource{head: "first-head"}
	service := newServiceWithAuthority(db, testAuthority{}, source)
	flag := confirmationVerdict(t, service, pid, DoesNotConform)
	if _, err := service.Confirm(caller("bls"), Confirmation{VerdictID: flag.ID, Note: "yes, that is marketing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(caller("bls"), Confirmation{VerdictID: flag.ID, Note: "again"}); err == nil {
		t.Fatal("a second confirm of the same verdict must be refused")
	}
	// A verdict nobody recorded is not found, in the owner's words.
	if _, err := service.Confirm(caller("bls"), Confirmation{VerdictID: idstest.N(1 << 40), Note: "none"}); !errors.Is(err, ErrNoVerdict) || fault.KindOf(err) != fault.NotFound || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("confirming an unknown verdict: %v", err)
	}
	if _, err := service.View(caller("reviewer"), idstest.N(1<<40)); !errors.Is(err, ErrNoReview) || fault.KindOf(err) != fault.NotFound {
		t.Fatalf("viewing an unknown review: %v", err)
	}
	view, err := service.View(caller("reviewer"), ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	ps := confirmationStanding(t, view, "proj")
	if ps.Lanes.Findings != 1 || ps.Standing != StandingDoesNot {
		t.Fatalf("confirmed flag is a finding: %+v", ps)
	}
	if ad, ok := view.Adjudications[flag.ID]; !ok || ad.By != "bls" || ad.Kind != "confirmed" {
		t.Fatalf("view must carry the adjudication: %+v", view.Adjudications)
	}

	source.head = "second-head"
	fresh := confirmationVerdict(t, service, pid, DoesNotConform)
	view, err = service.View(caller("reviewer"), ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	ps = confirmationStanding(t, view, "proj")
	if ps.Lanes.Flagged != 1 || ps.Lanes.Findings != 0 {
		t.Fatalf("a later verdict is not confirmed by an earlier adjudication: %+v", ps)
	}
	if pc, ok := view.PriorConfirmations[fresh.ID]; !ok || pc.By != "bls" || pc.SHA != flag.SHA {
		t.Fatalf("fresh flag must show prior confirmation: %+v", view.PriorConfirmations)
	}

	// Only an AI failure can be confirmed; a deterministic verdict from a real
	// owner review is already a finding.
	deterministic := createTestProject(t, db, "deterministic", "Deterministic", "main")
	shBlock := stockCheckFixture(t, db, wow.StockBlock{Name: "hygiene", ChecksJSON: `[{"name":"no-fixme","evaluator":"sh","sh":"exit 1","severity":"high"}]`})
	pinCheckFixture(t, db, deterministic, wow.ResolvedPin{BlockID: shBlock, Name: "hygiene", Version: 1})
	shVerdict := confirmationVerdict(t, service, deterministic, DoesNotConform)
	if shVerdict.Evaluator != "sh" || shVerdict.State != DoesNotConform {
		t.Fatalf("deterministic review verdict: %+v", shVerdict)
	}
	if _, err := service.Confirm(caller("bls"), Confirmation{VerdictID: shVerdict.ID}); err == nil {
		t.Fatal("a deterministic verdict needs no confirmation and must be refused")
	}
}
