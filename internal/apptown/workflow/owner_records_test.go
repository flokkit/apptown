package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

func TestTransactionalAdmissionRecordsTheVerifiedOwner(t *testing.T) {
	t.Parallel()
	admissions, req, def := runAdmissionFixture(t)
	actor := commandActor{Receipt: "verified via chat", InferenceOwner: "verified", Owner: access.Actor{Name: "verified", Via: "chat:c1"}}
	service := newService(admissions.records.db, actorAuthorityFunc(func(context.Context, operationPermission) (commandActor, error) { return actor, nil }), Runtime{})
	// Even an in-package forged value is replaced at the public boundary.
	req.requestedBy, req.owner, req.receiptActor = "forged", access.Actor{Name: "forged"}, "forged"
	run, err := database.Transact(t.Context(), admissions.records.db, func(tx *database.Tx) (*Run, error) { return service.AdmitRunIn(t.Context(), tx, req, def) })
	if err != nil {
		t.Fatal(err)
	}
	snap, err := service.Snapshot(t.Context(), run.ID, DetailView)
	if err != nil || len(snap.Steps) != 1 {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	_, got, err := ObserveStepAttemptIn(t.Context(), admissions.records.db, snap.Steps[0].ID, 0)
	if err != nil || got.Owner != "verified" {
		t.Fatalf("recorded owner: %+v %v", got, err)
	}
}

func TestLandingRecheckRecordsTheVerifiedActor(t *testing.T) {
	for _, transactional := range []bool{false, true} {
		t.Run(map[bool]string{false: "command", true: "transaction"}[transactional], func(t *testing.T) {
			t.Parallel()
			q, run, _ := landingRecheckOwnerFixture(t)
			service := newService(q.db, actorAuthorityFunc(func(context.Context, operationPermission) (commandActor, error) {
				return commandActor{Receipt: "verified via chat"}, nil
			}), Runtime{})
			service.landingRechecks = newLandingRechecks(q.db, []string{"scan"}, nil)
			in := LandingRecheck{ChangeID: run.ChangeID, who: "forged"}
			var err error
			if transactional {
				err = q.db.InTx(t.Context(), func(tx *database.Tx) error { _, err := service.RecheckLandingIn(t.Context(), tx, in); return err })
			} else {
				_, err = service.RecheckLanding(t.Context(), in)
			}
			if err != nil {
				t.Fatal(err)
			}
			snap, err := service.Snapshot(t.Context(), run.ID, DetailView)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range snap.Events {
				if event.Kind == "step.recheck" {
					var receipt landingRecheckReceipt
					if err := json.Unmarshal(event.Payload, &receipt); err != nil {
						t.Fatal(err)
					}
					if receipt.By != "verified via chat" {
						t.Fatalf("recheck actor: %+v", receipt)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("missing recheck receipt")
			}
		})
	}
}
