package openrouter_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/openrouter"
	"flokkit.io/forge/internal/openrouter/openrouterfake"
)

const provisioning = "sk-or-v1-PROVISIONING-SENTINEL"

func client(f *openrouterfake.Server) *openrouter.Client {
	return &openrouter.Client{Base: f.Base, ProvisioningKey: provisioning}
}

// The budget is the pair: both fields go on every create and change, and no
// reset is sent as an explicit null, so clearing a reset takes effect.
func TestBudgetSendsLimitAndResetTogether(t *testing.T) {
	f := openrouterfake.New(provisioning)
	defer f.Close()
	c := client(f)
	ctx := context.Background()
	k, value, err := c.Create(ctx, "forge-a-main-1", openrouter.Budget{Limit: 5, Reset: "daily"})
	if err != nil || value == "" || k.Hash == "" {
		t.Fatalf("create: %v %q %+v", err, value, k)
	}
	if err := c.SetBudget(ctx, k.Hash, openrouter.Budget{Limit: 5}); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	patch := calls[len(calls)-1]
	if v, present := patch.Body["limit_reset"]; !present || v != nil {
		t.Fatalf("no reset must be an explicit null, sent %v (present %v)", v, present)
	}
	if patch.Body["limit"] != 5.0 {
		t.Fatalf("the limit goes with every change: %v", patch.Body)
	}
	got, err := c.Get(ctx, k.Hash)
	if err != nil || got.Reset != nil || got.Limit == nil || *got.Limit != 5 {
		t.Fatalf("read back: %v %+v", err, got)
	}
}

// Errors carry the status and OpenRouter's short message, never either key.
func TestErrorsNameStatusNotSecrets(t *testing.T) {
	f := openrouterfake.New(provisioning)
	defer f.Close()
	c := client(f)
	ctx := context.Background()
	_, value, err := c.Create(ctx, "k", openrouter.Budget{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.FailNext("POST", 500)
	_, _, err = c.Create(ctx, "k2", openrouter.Budget{Limit: 1})
	var oe *openrouter.Error
	if !errors.As(err, &oe) || oe.Status != 500 {
		t.Fatalf("want a 500 *Error, got %v", err)
	}
	err = c.Delete(ctx, "nothere")
	if !errors.As(err, &oe) || !oe.NotFound() {
		t.Fatalf("want not found, got %v", err)
	}
	bad := &openrouter.Client{Base: f.Base, ProvisioningKey: "wrong"}
	_, err = bad.List(ctx)
	for _, e := range []error{err, oe} {
		if strings.Contains(e.Error(), provisioning) || strings.Contains(e.Error(), value) || strings.Contains(e.Error(), "wrong") {
			t.Fatalf("an error carries a key: %v", e)
		}
	}
}

// The allowance shown is the spend over the key's own reset period, not
// its lifetime spend.
func TestUsagePeriodFollowsTheReset(t *testing.T) {
	f := openrouterfake.New(provisioning)
	defer f.Close()
	c := client(f)
	ctx := context.Background()
	k, _, _ := c.Create(ctx, "k", openrouter.Budget{Limit: 5, Reset: "monthly"})
	f.SetUsage(k.Hash, 12, 1)
	got, err := c.Get(ctx, k.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsagePeriod() != 1 || got.Usage != 12 || got.Remaining == nil || *got.Remaining != 4 {
		t.Fatalf("period %v lifetime %v remaining %v", got.UsagePeriod(), got.Usage, got.Remaining)
	}
}
