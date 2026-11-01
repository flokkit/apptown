package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/apptown/jobs"
)

const HealthObservationJob = "integration-health"

// HealthChecker runs only in a collection job, never while a page reads.
// Binding configuration and version are captured before the check starts.
type HealthChecker interface {
	CheckBinding(context.Context, Binding) Health
}

// observeHealth records only a result for the binding version it checked.
// Reconfiguration while a provider is answering cannot renew the new binding.
func (s *Service) observeHealth(ctx context.Context, name string, checker HealthChecker) error {
	b, err := s.Bindings.ByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNoBinding, name)
	}
	if err != nil {
		return err
	}
	ctx, stop := context.WithTimeout(ctx, b.HealthCheckTimeout())
	defer stop()
	h := checker.CheckBinding(ctx, *b).ForDisplay()
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && b.HasAutomaticCheck() {
		h = Health{Detail: "The connection check timed out."}.At(s.now())
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.saveHealth(finish, *b, h)
}

// TestSaved runs the observation operation immediately for the saved settings.
// The periodic job uses the same checker and version-fenced write.
func (s *Service) TestSaved(ctx context.Context, name string, checker HealthChecker) (Health, error) {
	if err := s.observeHealth(ctx, name, checker); err != nil {
		return Health{}, err
	}
	return s.RecordedHealth(ctx, name)
}

func (s *Service) saveHealth(ctx context.Context, b Binding, h Health) error {
	// An unsupported automatic check cannot erase a real manual result.
	if !h.Checked && !h.NotSetUp {
		return nil
	}
	priorRaw, err := integrationsql.New(s.Bindings.db.SQL()).BindingHealth(ctx, b.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var prior Health
	if err == nil {
		if err := json.Unmarshal(priorRaw, &prior); err != nil {
			return err
		}
	}
	if h.StateLabel == HealthPassed {
		good := h
		good.LastGood = nil
		h.LastGood = &good
	} else {
		h.LastGood = prior.LastGood
		if h.StateLabel == HealthFailed {
			h.FailedAt, h.Failure = h.CheckedAt, h.Detail
		}
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return integrationsql.New(s.Bindings.db.SQL()).SaveBindingHealth(ctx, integrationsql.SaveBindingHealthParams{ID: b.ID, Version: int64(b.Version), Payload: raw})
}

// RecordedHealth is the sole reading used by the integration list and Attention.
// Results older than fifteen minutes show their age. Automatic results expire
// after six hours, including explicit tests. Kinds without an automatic
// check supply no Attention coverage.
func (s *Service) RecordedHealth(ctx context.Context, name string) (Health, error) {
	b, err := s.Bindings.ByName(ctx, name)
	if err != nil {
		return Health{}, err
	}
	automatic := b.HasAutomaticCheck()
	if !automatic {
		return Health{StateLabel: HealthUnknown, Detail: "No automatic check."}, nil
	}
	raw, err := integrationsql.New(s.Bindings.db.SQL()).BindingHealth(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return Health{Automatic: automatic, StateLabel: HealthUnknown, Detail: "Waiting for the first background check."}, nil
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
		if automatic && age > 6*time.Hour {
			h.StateLabel = HealthUnknown
			h.Expired = true
			h.Detail = "The last check is more than six hours old. An administrator can test this connection."
		}
	}
	return h, nil
}

// HealthObservationKey coalesces work only for the same binding settings.
func (b Binding) HealthObservationKey() string { return b.Name + "/" + strconv.Itoa(b.Version) }

func (s *Service) RunHealthObservation(ctx context.Context, key string, checker HealthChecker) error {
	name, version, ok := strings.Cut(key, "/")
	if !ok {
		return jobs.Permanent(errors.New("invalid integration observation identity"))
	}
	b, err := s.Bindings.ByName(ctx, name)
	if err != nil {
		return err
	}
	if strconv.Itoa(b.Version) != version {
		return nil
	}
	return s.observeHealth(ctx, name, checker)
}
