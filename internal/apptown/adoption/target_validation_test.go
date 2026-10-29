package adoption

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

func TestTargetAdmissionRejectsForgedAcceptanceAndRollsBackParticipants(t *testing.T) {
	t.Parallel()
	db, service, c := validationCandidate(t)
	// A target selects a procedure; installation is a separate direct command.
	c.Installation = Installation{}
	c.Receipt.Policies, c.Receipt.Extensions = nil, nil
	c.Digest = Digest(c.Receipt)
	service.deps.Authority = serviceAuthorityFunc(func(context.Context, access.Permission) error { return nil })
	// The empty-check publication is valid; nil and an empty list both mean none.
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := service.RecordTargetIn(adoptionTestCaller(), tx, Target{ProjectID: c.ProjectID, ChangeID: idstest.N(10), Candidate: c, Actor: "alice"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: change the exact immutable version already pinned by the target; BumpBlock would create a different version.
	if _, err := db.SQL().ExecContext(context.Background(), `UPDATE guidance_block_versions SET checks_json='[{"appliesTo": ["repository"], "name":"works","title":"Works","expects":"start succeeds"}]' WHERE block_id=$1 AND version=1`, c.Receipt.Guidance[0].BlockID); err != nil {
		t.Fatal(err)
	}
	err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if _, err := adoptionsql.New(tx.SQL()).SetCurrentDrift(context.Background(), adoptionsql.SetCurrentDriftParams{ProjectID: c.ProjectID, DriftNote: "participant wrote"}); err != nil {
			return err
		}
		_, err := service.RecordTargetIn(adoptionTestCaller(), tx, Target{ProjectID: c.ProjectID, ChangeID: idstest.N(11), Candidate: c, Actor: "alice"})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "target acceptance") {
		t.Fatalf("omitted checks admitted: %v", err)
	}
	if n := targetsOf(t, db, c.ProjectID, idstest.N(11)); n != 0 {
		t.Fatalf("target without checks stored: %d", n)
	}
	if cur := currentOf(t, db, c.ProjectID); cur.Drift != "" {
		t.Fatalf("refused target noted drift: %+v", cur)
	}
	c.NewChecks = []CheckSummary{{Block: "held-guide", Version: 1, Check: "works", Title: "Works", Expects: "start succeeds"}}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := service.RecordTargetIn(adoptionTestCaller(), tx, Target{ProjectID: c.ProjectID, ChangeID: idstest.N(11), Candidate: c, Actor: "alice"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := targetsOf(t, db, c.ProjectID, idstest.N(11)); n != 1 {
		t.Fatalf("target stored %d times", n)
	}
}

func TestTargetNewChecksPreserveChangeAndDeclarationOrder(t *testing.T) {
	t.Parallel()
	cat := candidateCatalogFake{blocks: map[ids.ID]wow.Block{idstest.N(1): {ID: idstest.N(1), Name: "held"}, idstest.N(2): {ID: idstest.N(2), Name: "changed"}, idstest.N(3): {ID: idstest.N(3), Name: "enabled"}, idstest.N(4): {ID: idstest.N(4), Name: "added"}}, versions: map[ids.ID]map[int]wow.BlockObservation{idstest.N(2): {2: {ChecksJSON: `[{"name":"first"},{"name":"second"}]`}}, idstest.N(3): {1: {ChecksJSON: `[{"name":"enabled"}]`}}, idstest.N(4): {1: {ChecksJSON: `[]`}}}}
	prior := Receipt{Guidance: []ReceiptBlock{{BlockID: idstest.N(1), Name: "held", Version: 1}, {BlockID: idstest.N(2), Name: "changed", Version: 1}, {BlockID: idstest.N(3), Name: "enabled", Version: 1, Disabled: true}}}
	next := Receipt{Guidance: []ReceiptBlock{{BlockID: idstest.N(3), Name: "enabled", Version: 1}, {BlockID: idstest.N(1), Name: "held", Version: 1}, {BlockID: idstest.N(2), Name: "changed", Version: 2}, {BlockID: idstest.N(4), Name: "added", Version: 1}}}
	got, err := (candidateBuilder{ways: cat}).newChecks(t.Context(), prior, next)
	want := []CheckSummary{{Block: "enabled", Version: 1, Check: "enabled"}, {Block: "changed", Version: 2, Check: "first"}, {Block: "changed", Version: 2, Check: "second"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("acceptance order/rules changed: %+v %v", got, err)
	}
}

// A check no landing can judge (one answered only by a standing deployment)
// is no uplift's acceptance: the adoption brings it, reviews judge it, and
// the uplift's gate is never asked to prove it.
func TestAnUpliftIsNotAcceptedOnACheckNoLandingCanJudge(t *testing.T) {
	t.Parallel()
	cat := candidateCatalogFake{blocks: map[ids.ID]wow.Block{idstest.N(1): {ID: idstest.N(1), Name: "deploy/memory-declared"}},
		versions: map[ids.ID]map[int]wow.BlockObservation{idstest.N(1): {1: {ChecksJSON: `[{"appliesTo": ["repository"], "name":"memory-declared","evaluator":"runtime","probe":"memory-declared"},{"appliesTo": ["repository"], "name":"memory-within","evaluator":"runtime","probe":"memory-within"},{"appliesTo": ["repository"], "name":"memory-within-implied","probe":"memory-within 80%"}]`}}}}
	next := Receipt{Guidance: []ReceiptBlock{{BlockID: idstest.N(1), Name: "deploy/memory-declared", Version: 1}}}
	got, err := (candidateBuilder{ways: cat}).newChecks(t.Context(), Receipt{}, next)
	want := []CheckSummary{{Block: "deploy/memory-declared", Version: 1, Check: "memory-declared"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("the uplift's acceptance: %+v %v", got, err)
	}
}
