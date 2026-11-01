package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
)

const HealthObservationJob = "integration-health"

// HealthChecker runs only in a collection job, never while a page reads.
// Binding configuration and version are captured before the check starts.
type HealthChecker interface {
	CheckBinding(context.Context, Binding) Health
}

// ObserveHealth records only a result for the binding version it checked.
// Reconfiguration while a provider is answering cannot renew the new binding.
func (s *Service) ObserveHealth(ctx context.Context, name string, checker HealthChecker) error {
	b, err := s.Bindings.ByName(ctx, name)
	if err != nil {
		return err
	}
	h := checker.CheckBinding(ctx, *b).ForDisplay()
	return s.saveHealth(ctx, *b, h)
}

// BindingTester checks saved configuration on an administrator's request.
// Draft tests use the provider test port without writing a binding reading.
type BindingTester interface {
	TestBinding(context.Context, Binding) Health
}

// TestSaved records a test for the exact saved binding version it reads. A
// later configuration change rejects that result through the same write as jobs.
func (s *Service) TestSaved(ctx context.Context, name string, tester BindingTester) (Health, error) {
	b, err := s.Bindings.ByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return Health{}, fmt.Errorf("%w: %s", ErrNoBinding, name)
	}
	if err != nil {
		return Health{}, err
	}
	h := tester.TestBinding(ctx, *b).ForDisplay()
	h.Manual = true
	if !h.Checked && !h.NotSetUp {
		return h, nil
	}
	if err := s.saveHealth(ctx, *b, h); err != nil {
		return Health{}, err
	}
	return s.RecordedHealth(ctx, name)
}

func (s *Service) saveHealth(ctx context.Context, b Binding, h Health) error {
	// An unsupported automatic check cannot erase a real manual result.
	if !h.Checked && !h.NotSetUp {
		return nil
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return integrationsql.New(s.Bindings.db.SQL()).SaveBindingHealth(ctx, integrationsql.SaveBindingHealthParams{ID: b.ID, Version: int64(b.Version), Payload: raw})
}

// RecordedHealth is the sole reading used by the integration list and Attention.
// Results older than fifteen minutes show their age. Automatic results expire
// after six hours; a manual test retains its result, marked old. Unsupported
// automatic checks supply no Attention coverage.
func (s *Service) RecordedHealth(ctx context.Context, name string) (Health, error) {
	b, err := s.Bindings.ByName(ctx, name)
	if err != nil {
		return Health{}, err
	}
	automatic := b.HasAutomaticCheck()
	raw, err := integrationsql.New(s.Bindings.db.SQL()).BindingHealth(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return Health{Automatic: automatic, StateLabel: HealthUnknown, Detail: "No check has been recorded yet. An administrator can test this connection."}, nil
	}
	if err != nil {
		return Health{}, err
	}
	var h Health
	if err := json.Unmarshal(raw, &h); err != nil {
		return Health{}, err
	}
	h.Automatic = automatic
	if h.StateLabel == HealthNotSetUp {
		h.NotSetUp = true
		uses, err := s.Attachments.ProjectsByBinding(ctx)
		if err != nil {
			return Health{}, err
		}
		if len(uses[name]) > 0 {
			h.NotSetUp = false
			h.StateLabel = HealthFailed
			h.Detail = "A required integration is not set up. An administrator must add its configuration."
		}
	}
	h.LastResult = h.StateLabel
	at, err := time.Parse(time.RFC3339, h.CheckedAt)
	if err == nil {
		age := s.now().Sub(at)
		h.Stale = age > 15*time.Minute
		if automatic && !h.Manual && age > 6*time.Hour {
			h.StateLabel = HealthUnknown
			h.Detail = "The last check is more than six hours old. An administrator can test this connection."
		}
	}
	return h, nil
}
