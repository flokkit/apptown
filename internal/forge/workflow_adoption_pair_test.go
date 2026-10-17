package forge

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestLandingAdoptionPairUsesScopedOwnerRecordsInTransaction(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"success", "foreign source", "foreign destination", "missing", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			project := plainTestProject(t, a.DB, "pair", "Pair")
			other := plainTestProject(t, a.DB, "other", "Other")
			a.DB.Pool().SetMaxOpenConns(1)
			err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
				fromProject, toProject := project, project
				if test == "foreign source" {
					fromProject = other
				}
				if test == "foreign destination" {
					toProject = other
				}
				from, err := recordPairRevision(t, tx, fromProject, 3)
				if err != nil {
					return err
				}
				to, err := recordPairRevision(t, tx, toProject, 4)
				if err != nil {
					return err
				}
				if test == "missing" {
					to = idstest.N(999999)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if test == "cancelled" {
					cancel()
				}
				out, err := (landingAdoptions{}).RevisionsIn(ctx, tx, workflow.AdoptionPairRequest{ProjectID: project, FromID: from, ToID: to})
				if test == "success" {
					if err != nil || out != (workflow.AdoptionPair{From: 3, To: 4}) {
						t.Fatalf("pair %+v: %v", out, err)
					}
				} else {
					want := sql.ErrNoRows
					if test == "cancelled" {
						want = context.Canceled
					}
					if !errors.Is(err, want) || out != (workflow.AdoptionPair{}) {
						t.Fatalf("invalid pair %+v: %v", out, err)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func recordPairRevision(t *testing.T, tx *database.Tx, projectID ids.ID, revision int64) (ids.ID, error) {
	t.Helper()
	current, err := adoption.CurrentIn(context.Background(), tx, projectID)
	var previous int64
	if err == nil {
		previous = current.Revision
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, err
	}
	for next := previous + 1; next <= revision; next++ {
		if err := adoption.RecordIn(t.Context(), tx, projectID, next, adoption.AdoptionCreate, "pair fixture", adoption.EmptyReceipt(), "fixture", time.Now()); err != nil {
			return ids.Nil, err
		}
	}
	return adoption.CurrentReceiptIDIn(context.Background(), tx, projectID)
}
