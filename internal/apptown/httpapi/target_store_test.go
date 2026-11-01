package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/ids"
)

func targetStatusIs(t *testing.T, w *targetHTTPWorld, id ids.ID, want deployment.TargetState) {
	t.Helper()
	got, err := w.deployment.Targets.ByID(t.Context(), id)
	if err != nil || got.Status != want {
		t.Fatalf("target status %v want %v: %v", got, want, err)
	}
}

func targetOffersAre(t *testing.T, w *targetHTTPWorld, id ids.ID, want int) {
	t.Helper()
	offers, err := w.deployment.Targets.Offers(context.Background(), id)
	if err != nil || len(offers) != want {
		t.Fatalf("target offers %d want %d: %v", len(offers), want, err)
	}
}

func targetRetirementFixture(t *testing.T) (*targetHTTPWorld, *deployment.Target) {
	t.Helper()
	w := newTargetHTTPWorld(t)
	if _, err := w.integrations.Bindings.Save(context.Background(), "retiring-account", digitalocean.CapCloudAccount, "digitalocean", `{}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	target, err := w.deployment.Targets.Put(context.Background(), &deployment.Target{Name: "retiring-target", Kind: deployment.KindDOApp, Provider: "retiring-account", Origin: deployment.OriginAttached, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"*", "team"} {
		if err := w.deployment.Targets.Offer(context.Background(), target, group, deployment.OfferCaps{}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	return w, target
}

func TestTargetLateCheckCannotUndoRetirement(t *testing.T) {
	t.Parallel()
	w := newTargetHTTPWorld(t)
	target, err := w.deployment.Targets.Put(context.Background(), &deployment.Target{Name: "slow-check", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	w.docker.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) == 0 || args[0] != "info" {
			return nil, nil, false
		}
		// sql-guarantee: The checked snapshot must become attached before RecordCheck; Put preserves origin, so direct UPDATE isolates this retirement race.
		if _, err := w.db.SQL().ExecContext(context.Background(), `UPDATE targets SET origin='attached' WHERE id=$1`, target.ID); err != nil {
			t.Fatal(err)
		}
		if err := w.deployment.Targets.Retire(context.Background(), target.Name, "alice"); err != nil {
			t.Fatal(err)
		}
		return []byte("ready"), nil, true
	})
	code, _ := targetCall(t, http.HandlerFunc(w.targets.CheckTarget), "POST", "/check", "", map[string]string{"name": target.Name})
	if code != 409 {
		t.Fatalf("late check: %d", code)
	}
	targetStatusIs(t, w, target.ID, deployment.TargetRetired)
	for _, receipt := range receiptsOfKind(t, w.db, "target.checked") {
		if receipt.Subject == target.Name {
			t.Fatalf("late check left a receipt: %+v", receipt)
		}
	}
}

func TestTargetRetirementReadFailureIsNotPermissionOrAbsence(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"-- name: TargetByNameForUpdate", "-- name: BoundTargetDeployments", "-- name: ProviderHasLiveTargets"} {
		t.Run(query, func(t *testing.T) {
			w, target := targetRetirementFixture(t)
			t.Cleanup(dbtest.NextStatementFailure(w.db, query))
			code, _ := targetCall(t, http.HandlerFunc(w.targets.RetireTarget), "POST", "/retire", `{"confirm":"retiring-target"}`, map[string]string{"name": target.Name})
			if code != 500 {
				t.Fatalf("read failure: %d", code)
			}
			targetStatusIs(t, w, target.ID, deployment.TargetReady)
			targetOffersAre(t, w, target.ID, 2)
		})
	}
}
