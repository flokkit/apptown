package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/fault"
)

// One server's AppTown records no intranet target. An intranet release
// there is refused in words that say so and name the release that works,
// never with the database's "no rows".
//
// Kills: "the intranet target is not recorded: sql: no rows in result set"
// (one-server test, 22 Oct).
func TestNoIntranetTargetIsSaidPlainly(t *testing.T) {
	t.Parallel()
	s := New(context.Background(), testDB(t), Dependencies{})
	_, err := s.IntranetConfig(t.Context())
	if !errors.Is(err, ErrNoIntranet) || fault.KindOf(err) != fault.Conflict {
		t.Fatalf("no intranet target: %v", err)
	}
	if msg := err.Error(); strings.Contains(msg, "sql") || !strings.Contains(msg, "apptown release <project> <deployment>") {
		t.Fatalf("the refusal reads %q", msg)
	}
}
