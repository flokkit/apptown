package dataprotection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/deploy"
)

func TestLostFirstDeleteDoesNotClaimNothingDeleted(t *testing.T) {
	s, _, driver, dep := removalFixture(t)
	ops, _ := takeDownOps(t, s)
	owned := deploy.Owned{Kind: "app", ID: "lost-answer-app"}
	if err := s.Deployments.OwnerOf(dep).Created(t.Context(), owned); err != nil {
		t.Fatal(err)
	}
	deleted := false
	driver.remove = func(context.Context, deploy.Placement, int64) error {
		deleted = true // Provider applied the first deletion but lost its answer.
		return errors.New("lost delete response")
	}
	op, err := s.TakeDown(t.Context(), removeSelection(dep))
	if err != nil {
		t.Fatal(err)
	}
	done, err := ops.RunHere(t.Context(), op.ID)
	if err != nil || !deleted || done.State != operations.Unknown {
		t.Fatalf("lost answer: %+v %v, deleted=%v", done, err, deleted)
	}
	if strings.Contains(done.Detail, "nothing deleted") {
		t.Fatalf("claims no deletion despite an unknown applied effect: %s", done.Detail)
	}
}
