package verification

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/stock"
)

// A baseline with unknowns is asked again once unknownRetry has passed on
// AppTown's clock: kept a minute on, asked again three minutes on, with
// that clock an hour ahead of the real one.
//
// Kills: the retry judged by the real clock (an hour behind the baseline's
// time, it never reads as due).
func TestABaselineWithUnknownsIsAskedAgainByAppTownsClock(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	at := time.Now().Add(time.Hour)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	move := func(d time.Duration) { mu.Lock(); at = at.Add(d); mu.Unlock() }
	changes := NewChanges(ChangeDependencies{DB: testDB(t), DataDir: t.TempDir(), Now: now})
	ctx := context.Background()
	checks := []conformance.DeclaredCheck{{Block: "b", Version: 1, Check: conformance.CheckDeclaration{AppliesTo: []string{"repository"}, Name: "check", Evaluator: "sh"}}}
	// A baseline asked now that could not evaluate one check.
	first := &baselineSet{failing: map[string]bool{}, items: map[string][]string{}, unknown: map[string]bool{"b@1|check": true}, sha: "base", at: now()}
	tools := stock.Versions().Current()
	changes.baseline.entries[fmt.Sprintf("%d|%s|%s|%s|%s", idstest.N(1), "base", checksDigest(checks), changes.toolIdentities(ctx, checks), tools.Digest)] = first
	move(time.Minute)
	if again := changes.baselineFor(ctx, tools, idstest.N(1), checks, "base", ""); again != first {
		t.Fatal("a baseline with unknowns was asked again within unknownRetry")
	}
	move(2 * time.Minute)
	if again := changes.baselineFor(ctx, tools, idstest.N(1), checks, "base", ""); again == first {
		t.Fatal("a baseline with unknowns was kept past unknownRetry")
	}
}
