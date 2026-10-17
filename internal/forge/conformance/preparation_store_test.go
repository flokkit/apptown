package conformance

import (
	"context"
	"errors"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/stamp"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"

	"fmt"

	"testing"
)

func preparationFixture(t *testing.T) (reviewStore, ids.ID, ids.ID, []ids.ID) {
	t.Helper()
	s, p, rs := conformanceHistoryFixtureWithOutcome(t, DoesNotConform)
	if err := project.New(s.db, nil).SetDefaultBranch(context.Background(), p, "main"); err != nil {
		t.Fatal(err)
	}
	block := stockCheckFixture(t, s.db, wow.StockBlock{Name: "standard", Body: "old body", ChecksJSON: `[{"name":"clear","evaluator":"llm","scope":"tree","expects":"Pinned wording","rubric":"Check clarity"},{"name":"unscanned","sh":"exit 0"}]`})
	if _, err := wow.NewLibrary(s.db).StockUpdate(t.Context(), wow.StockBlock{Name: "standard", Body: "new body", ChecksJSON: `[{"name":"new-check","sh":"exit 0"}]`}); err != nil {
		t.Fatal(err)
	}
	pinCheckFixture(t, s.db, p, wow.ResolvedPin{BlockID: block, Name: "standard", Version: 1})
	return s, p, block, rs
}

func TestCurrentPreparationBatchesPinnedEvidence(t *testing.T) {
	t.Parallel()
	s, p, block, rs := preparationFixture(t)
	counted := &countedQueries{Handle: s.db.SQL()}
	for _, n := range []int{1, 12} {
		if n > 1 {
			for i := 1; i < n; i++ {
				id := createTestProject(t, s.db, fmt.Sprintf("project-%02d", i), fmt.Sprintf("Project %d", i), "main")
				pinCheckFixture(t, s.db, id, wow.ResolvedPin{BlockID: block, Name: "standard", Version: 1})
				if _, err := queries(s.db).InsertVerdict(context.Background(), conformancesql.InsertVerdictParams{ID: ids.New(stamp.Of(time.Now()).Time), ReviewID: rs[len(rs)-1], ProjectID: id, ProjectSlug: fmt.Sprintf("project-%02d", i), Sha: "latest", Block: "standard", Version: 1, CheckName: "clear", Evaluator: "llm", EvaluatorVer: "v1", Mode: "posture", State: DoesNotConform.String(), CreatedAt: stamp.Of(time.Now()).Time}); err != nil {
					t.Fatal(err)
				}
			}
		}
		counted.reads = 0
		facts, err := readCurrentConformance(context.Background(), counted, testCheckSource{}, time.Now())
		if err != nil || len(facts.Projects) != n || len(facts.Verdicts) != n {
			t.Fatalf("batch %d: %+v %v", n, facts, err)
		}
		if counted.reads != 7 {
			t.Fatalf("%d projects used %d reads, want seven", n, counted.reads)
		}
		calls := 0
		view, err := currentConformanceView(context.Background(), facts, func(_ context.Context, p Project) (SourceHead, error) {
			calls++
			for _, v := range facts.Verdicts {
				if v.ProjectID == p.ID {
					return SourceHead{SHA: v.SHA}, nil
				}
			}
			t.Fatal("head requested without evidence")
			return SourceHead{}, nil
		})
		if err != nil || calls != n || len(view.Projects) != n || view.Summary.Flagged != n || view.Summary.Checks != 2 {
			t.Fatalf("view: %+v calls=%d %v", view, calls, err)
		}
		if d := view.CheckDefs["standard@1·clear"]; d.Expects != "Pinned wording" || len(d.Adopters) != n {
			t.Fatalf("unpinned/missing definition: %+v", d)
		}
		for _, project := range facts.Projects {
			if len(project.Checks) != 2 || project.Checks[0].Version != 1 {
				t.Fatalf("not pinned: %+v", project)
			}
		}
	}
	// An explicit empty selection is different from the whole fleet.
	if projects, err := s.prepareProjects(context.Background(), []ids.ID{}); err != nil || len(projects) != 0 {
		t.Fatalf("empty selection: %+v %v", projects, err)
	}
	if projects, err := s.prepareProjects(context.Background(), []ids.ID{p}); err != nil || len(projects) != 1 || projects[0].ID != p {
		t.Fatalf("project selection: %+v %v", projects, err)
	}
}

func TestPreparationRejectsUnavailableDefinitionsAndFailedReads(t *testing.T) {
	t.Parallel()
	s, p, block, _ := preparationFixture(t)
	for _, fragment := range []string{"FROM projects", "FROM guidance_imports", "FROM conformance_verdicts", "FROM dispositions", "FROM conformance_adjudications", "JOIN LATERAL", "FROM conformance_reviews"} {
		dbtest.FailNextStatement(t, s.db, fragment)
		if facts, err := s.current(context.Background()); err == nil || facts != nil {
			t.Fatalf("failed %s returned a view: %+v %v", fragment, facts, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if facts, err := s.current(ctx); !errors.Is(err, context.Canceled) || facts != nil {
		t.Fatalf("cancelled view: %+v %v", facts, err)
	}
	pinCheckFixture(t, s.db, p, wow.ResolvedPin{BlockID: block, Name: "standard", Version: 99})
	if facts, err := s.current(context.Background()); err == nil || facts != nil {
		t.Fatalf("missing pinned definition: %+v %v", facts, err)
	}
	pinCheckFixture(t, s.db, p, wow.ResolvedPin{BlockID: block, Name: "standard", Version: 99, Disabled: true})
	if facts, err := s.current(context.Background()); err != nil || len(facts.Projects) != 1 || len(facts.Projects[0].Checks) != 0 {
		t.Fatalf("disabled missing definition: %+v %v", facts, err)
	}
	pinCheckFixture(t, s.db, p, wow.ResolvedPin{BlockID: block, Name: "standard", Version: 1})
	malformed := stockCheckFixture(t, s.db, wow.StockBlock{Name: "malformed", ChecksJSON: `{}`})
	pinCheckFixture(t, s.db, p, wow.ResolvedPin{BlockID: malformed, Name: "malformed", Version: 1})
	if facts, err := s.current(context.Background()); err == nil || facts != nil {
		t.Fatalf("malformed declaration: %+v %v", facts, err)
	}
}

func TestPreparationReadsOneDatabaseSnapshot(t *testing.T) {
	t.Parallel()
	s, p, block, _ := preparationFixture(t)
	_, err := readConformance(context.Background(), s.db, func(db database.Queryer) (bool, error) {
		if _, err := readConformanceProjects(context.Background(), db, []ids.ID{p}, true); err != nil {
			return false, err
		}
		pinCheckFixture(t, s.db, p, wow.ResolvedPin{BlockID: block, Name: "standard", Version: 2})
		checks, err := readAdoptedChecks(context.Background(), db, testCheckSource{}, []ids.ID{p})
		if err != nil {
			return false, err
		}
		if len(checks[p]) != 2 || checks[p][0].Version != 1 {
			t.Fatalf("adoption escaped snapshot: %+v", checks)
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checks, err := readAdoptedChecks(context.Background(), s.db, testCheckSource{}, []ids.ID{p})
	if err != nil || len(checks[p]) != 1 || checks[p][0].Version != 2 {
		t.Fatalf("next snapshot missed adoption: %+v %v", checks, err)
	}
}

// An eval copy is never assessed: Conformance's project read leaves it out
// by Project's purpose, whether or not it is repo-backed.
func TestConformanceLeavesEvalCopiesOut(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	real := createTestProject(t, db, "shop", "Shop", "main")
	copy, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "shop-copy", Purpose: project.EvalCopy})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.New(db, nil).SetDefaultBranch(context.Background(), copy, "main"); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]ids.ID{nil, {real, copy}} {
		got, err := readConformanceProjects(context.Background(), db, ids, true)
		if err != nil || len(got) != 1 || got[0].ID != real {
			t.Fatalf("projects %v: %+v %v", ids, got, err)
		}
	}
}
