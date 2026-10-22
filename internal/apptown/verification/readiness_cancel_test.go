package verification

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

// A readiness probe whose contract read is cut short by its caller cannot
// assess: the stopped read must not become the undeclared-readiness finding.
// (Observe, the review's probe, follows the same rule; it reaches it only
// through a standing deployment.)
func TestAStoppedReadinessReadIsNotAnUndeclaredReadiness(t *testing.T) {
	r := Runtime{dataDir: t.TempDir()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	target := Target{Slug: "shop", ReleaseSHA: "0123456789abcdef0123456789abcdef01234567"}

	if v := r.Probe(ctx, target, "readiness"); v.Outcome != check.CannotAssess || v.Detail != readinessReadStopped {
		t.Errorf("gate probe after its caller stopped: %+v, want cannot assess", v)
	}
}
