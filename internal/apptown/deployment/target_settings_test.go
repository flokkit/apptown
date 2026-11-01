package deployment

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
)

// Two edits of a target's settings from one reading: exactly one saves, the
// other is refused and leaves the winner's values. A routine status check
// in between moves nothing, and startup sync moves the revision only when it
// really changes a setting.
func TestTargetSettingsEditsFromOneRevisionOneWinsAndChecksDoNotMoveIt(t *testing.T) {
	t.Parallel()
	_, targets, target := targetRetirementFixture(t)
	ctx := context.Background()
	if _, err := targets.RecordCheck(ctx, target, true, "health", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	rev, yes, mode := int64(0), true, "managed"
	edits := []TargetSettingsEdit{{ExpectedRevision: &rev, Previews: &yes}, {ExpectedRevision: &rev, NeedsMode: &mode}}
	errs := make([]error, len(edits))
	var wg sync.WaitGroup
	for i, e := range edits {
		wg.Go(func() { _, errs[i] = targets.EditSettings(ctx, target.Name, e, "alice") })
	}
	wg.Wait()
	saved, moved := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			saved++
		case errors.Is(err, errTargetSettingsMoved):
			moved++
		default:
			t.Fatal(err)
		}
	}
	if saved != 1 || moved != 1 {
		t.Fatalf("want one saved and one refused: %v", errs)
	}
	after, err := targets.ByName(ctx, target.Name)
	if err != nil || after.SettingsRevision != 1 {
		t.Fatalf("revision: %+v %v", after, err)
	}
	if errs[0] == nil && (!after.Capabilities.Previews || after.NeedsMode == mode) || errs[1] == nil && (after.NeedsMode != mode || after.Capabilities.Previews) {
		t.Fatalf("the refused edit leaked: %+v", after)
	}
	if _, err := targets.EditSettings(ctx, target.Name, TargetSettingsEdit{Previews: &yes}, "alice"); !errors.Is(err, errTargetSettingsRevisionRequired) {
		t.Fatalf("edit without a revision: %v", err)
	}
	// Startup sync writes the same settings back: no conflict for an open edit.
	if _, err := targets.sync(ctx, after, "forge"); err != nil {
		t.Fatal(err)
	}
	if again, _ := targets.ByName(ctx, target.Name); again.SettingsRevision != 1 {
		t.Fatalf("an unchanged sync moved the revision: %d", again.SettingsRevision)
	}
	after.Capabilities.Rollout = []string{"rolling"}
	if _, err := targets.sync(ctx, after, "forge"); err != nil {
		t.Fatal(err)
	}
	if again, _ := targets.ByName(ctx, target.Name); again.SettingsRevision != 2 {
		t.Fatalf("a changing sync kept the revision: %d", again.SettingsRevision)
	}
}

// A verified endpoint is immutable even when an older named integration or
// environment display label changes. A new target is required to retarget.
func TestVerifiedPortainerEndpointCannotBeRetargetedBySettings(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	ctx := t.Context()
	intranet := p.target(t, IntranetTargetName)
	office := "office"
	if _, err := p.s.Targets.EditSettings(ctx, IntranetTargetName, TargetSettingsEdit{ExpectedRevision: &intranet.SettingsRevision, Environment: &office}, "alice"); err == nil {
		t.Fatal("verified endpoint accepted a different environment")
	}
	saved := p.target(t, IntranetTargetName)
	if saved.SettingsRevision != intranet.SettingsRevision || saved.Address[addressEnvironment] != intranet.Address[addressEnvironment] {
		t.Fatal("refused retarget changed saved settings")
	}
	if receipts := operationReceipts(t, p.s.db, "target.edited", ids.Nil); len(receipts) != 0 {
		t.Fatal("refused retarget recorded a successful edit")
	}
	cfg, err := p.s.providers.PortainerConfig(ctx, saved)
	if err != nil || cfg.Scope == nil || cfg.Scope.EndpointID != 3 {
		t.Fatalf("verified selection lost: %+v %v", cfg, err)
	}
	blank := " "
	if _, err := p.s.Targets.EditSettings(ctx, IntranetTargetName, TargetSettingsEdit{ExpectedRevision: &saved.SettingsRevision, Environment: &blank}, "alice"); !errors.Is(err, errTargetEnvironment) {
		t.Fatalf("blank environment: %v", err)
	}
	lab := p.target(t, "lab")
	if _, err := p.s.Targets.EditSettings(ctx, "lab", TargetSettingsEdit{ExpectedRevision: &lab.SettingsRevision, Environment: &office}, "alice"); !errors.Is(err, errTargetEnvironment) {
		t.Fatalf("Docker environment: %v", err)
	}
}

func TestTargetDomainEditRejectsURLComponentsWithoutChangingSettings(t *testing.T) {
	_, targets, target := targetRetirementFixture(t)
	valid := "*.apps.example"
	changed, err := targets.EditSettings(t.Context(), target.Name, TargetSettingsEdit{ExpectedRevision: &target.SettingsRevision, Domain: &valid}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"apps.example?x=1", "apps.example#fragment", "person@apps.example"} {
		_, err := targets.EditSettings(t.Context(), target.Name, TargetSettingsEdit{ExpectedRevision: &changed.SettingsRevision, Domain: &domain}, "alice")
		if !errors.Is(err, errTargetDomain) {
			t.Fatalf("invalid domain %q accepted: %v", domain, err)
		}
		current, err := targets.ByName(t.Context(), target.Name)
		if err != nil {
			t.Fatal(err)
		}
		if current.SettingsRevision != changed.SettingsRevision || current.Ingress["domain"] != "apps.example" {
			t.Fatalf("rejected domain changed settings: %+v", current)
		}
	}
	empty := ""
	cleared, err := targets.EditSettings(t.Context(), target.Name, TargetSettingsEdit{ExpectedRevision: &changed.SettingsRevision, Domain: &empty}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cleared.Ingress["domain"]; exists {
		t.Fatal("empty domain did not clear setting")
	}
}
