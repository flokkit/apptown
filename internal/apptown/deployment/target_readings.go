package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/ids"
)

const TargetObservationJob = "target-observation"

type TargetReading struct {
	DetailReading
	ConsecutiveFailures int              `json:"consecutiveFailures"`
	OK                  bool             `json:"ok"`
	Detail              string           `json:"detail"`
	Registry            *RegistryReading `json:"registry,omitempty"`
	LastGood            *TargetReading   `json:"lastGood,omitempty"`
}

func (s *Targets) RecordedCheck(ctx context.Context, t *Target) (TargetReading, error) {
	var out TargetReading
	row, err := deploymentsql.New(s.db.SQL()).Reading(ctx, deploymentsql.ReadingParams{Subject: t.ID.String(), Kind: "target-health"})
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Identity != strconv.FormatInt(t.SettingsRevision, 10) {
		out.Unknown = true
		out.Failure = "Waiting for the first background check."
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(row.Payload, &out); err != nil {
		return out, err
	}
	out.Stale = out.ObservedAt.IsZero() || s.now().Sub(out.ObservedAt) > 15*time.Minute
	out.Unknown = out.AttemptedAt.IsZero() || s.now().Sub(out.AttemptedAt) > 6*time.Hour
	return out, nil
}

// ObserveTarget is the single check operation for periodic and explicit requests.
// recordCheck fences retirement and settings edits before recording the answer.
func (p *Providers) ObserveTarget(ctx context.Context, t *Target, who string) (TargetReading, error) {
	prior, err := p.targets.RecordedCheck(ctx, t)
	if err != nil {
		return prior, err
	}
	got := p.checkTargetStatus(ctx, t)
	if errors.Is(ctx.Err(), context.Canceled) {
		return prior, ctx.Err()
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	out := TargetReading{DetailReading: DetailReading{AttemptedAt: p.targets.now()}, OK: got.OK, Detail: got.Detail, Registry: got.Registry, LastGood: prior.LastGood}
	if got.OK {
		out.ObservedAt = out.AttemptedAt
		good := out
		good.LastGood = nil
		out.LastGood = &good
	} else {
		out.ConsecutiveFailures = prior.ConsecutiveFailures + 1
		out.ObservedAt = prior.ObservedAt
		out.Failure = got.Detail
	}
	if got.OK || who != "background check" || out.ConsecutiveFailures >= 2 {
		_, err = p.targets.recordCheck(finish, t, got.OK, who, map[string]any{"ok": got.OK, "detail": got.Detail, "registry": got.Registry})
		if err != nil {
			return out, err
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	err = deploymentsql.New(p.targets.db.SQL()).StoreReading(finish, deploymentsql.StoreReadingParams{ID: ids.New(p.targets.now()), Subject: t.ID.String(), Kind: "target-health", Identity: strconv.FormatInt(t.SettingsRevision, 10), Payload: raw})
	return out, err
}

func (p *Providers) RunTargetObservation(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	raw, rev, ok := strings.Cut(key, "/")
	id, err := ids.Parse(raw)
	if err != nil || !ok {
		return nil, jobs.Permanent(errors.New("invalid target observation identity"))
	}
	t, err := p.targets.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status == TargetRetired || strconv.FormatInt(t.SettingsRevision, 10) != rev {
		return nil, nil
	}
	_, err = p.ObserveTarget(ctx, t, "background check")
	return nil, err
}
