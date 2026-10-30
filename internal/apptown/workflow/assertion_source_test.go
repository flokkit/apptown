package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

type assertionADRPermit struct{}

func (assertionADRPermit) Require(context.Context, access.Permission) (access.Caller, error) {
	return access.Caller{Principal: access.Principal{Name: "fixture"}, Verified: true}, nil
}

func assertionOwnerFixture(t *testing.T, typ, cfg string) (*Assertions, *Run, ids.ID) {
	t.Helper()
	var config map[string]any
	decoder := json.NewDecoder(strings.NewReader(cfg))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}
	// Three admission receipts remain visible to every all-event assertion.
	q, run, id := admitSingleStep(t, schema.Step{Name: "assert", Uses: typ, With: config})
	if err := q.db.InTx(context.Background(), func(tx *database.Tx) error {
		return adoption.InitializeIn(t.Context(), tx, run.ProjectID, 0, "")
	}); err != nil {
		t.Fatal(err)
	}
	return NewAssertions(q.db, nil), run, id
}
func TestAssertionSourceFactsStayLockedUntilResultCommit(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"artifact/require", "adr/require"} {
		t.Run(typ, func(t *testing.T) {
			t.Parallel()
			s, run, id := assertionOwnerFixture(t, typ, `{}`)
			// sql-guarantee: the concurrent source-lock law deletes the already-observed artifact on another connection after the snapshot; artifact storage has no delete operation or generated mutation.
			mutation := "DELETE FROM artifacts"
			if typ == "artifact/require" {
				insertTextArtifactForTest(t, s.db, run.ID, "report", "", "", "", time.Time{})
			} else {
				decisions := adrs.New(s.db, assertionADRPermit{}, nil, nil)
				caller := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Name: "fixture"}, Verified: true})
				decision, err := decisions.Create(caller, adrs.Create{ProjectID: run.ProjectID, ChangeID: run.ChangeID, Title: "Decision"})
				if err != nil {
					t.Fatal(err)
				}
				for _, move := range []adrs.Decision{adrs.Propose, adrs.Accept} {
					decision, err = decisions.Edit(caller, adrs.Edit{ID: decision.ID, Decision: move})
					if err != nil {
						t.Fatal(err)
					}
				}
				// sql-guarantee: the concurrent source-lock law changes an accepted ADR to rejected after the snapshot; the ADR lifecycle forbids that transition and its generated mutation is private to the ADR owner.
				mutation = "UPDATE adrs SET status='rejected'"
			}
			step, err := stepResult(workflowsql.New(s.db.SQL()).StepByID(context.Background(), id))
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			outcome, err := evaluateAssertionIn(context.Background(), tx, run, step, nil)
			if err != nil || outcome.state != StatusPass {
				t.Fatalf("source observation: %+v %v", outcome, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			changed := make(chan error, 1)
			go func() { _, err := s.db.SQL().ExecContext(ctx, mutation); changed <- err }()
			dbtest.WaitForLock(t, s.db, mutation)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-changed; err != nil {
				t.Fatalf("source edit did not resume after commit: %v", err)
			}
		})
	}
}

// This command-only double deliberately cannot authorize aggregate reads.
func (assertionADRPermit) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, access.ErrAuthorityUnavailable
}
