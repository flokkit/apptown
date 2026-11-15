package changeactions

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// packCatchUp starts a project's assessment through the nightly catch-up path.
type packCatchUp interface {
	CatchUpProject(context.Context, ids.ID) (ids.ID, error)
}

type JanitorRun struct {
	Queue    string `json:"queue"`
	Again    bool   `json:"again"`
	ChangeID ids.ID `json:"changeId,omitzero"`
}

type JanitorStart struct {
	Queue            string `json:"queue"`
	Workflow         string `json:"workflow"`
	ChangeID         ids.ID `json:"changeId,omitzero"`
	ChangeNumber     int64  `json:"changeNumber"`
	WhatIfID         ids.ID `json:"whatIfId,omitzero"`
	Current          bool   `json:"current"`
	Refused          string `json:"refused"`
	BusyChangeID     ids.ID `json:"busyChangeId,omitzero"`
	BusyChangeNumber int64  `json:"busyChangeNumber"`
}

// RunJanitor fires the adopted standing queues through their nightly starts.
// Again resolves matching eligible starts first, then ends the named queue's
// episode once before any start, checking the expected open Change and close
// authority. No eligible start means no reset. A later start failure leaves
// the episode ended.
// Admission refusals are per-queue results; authority and read failures are errors.
func (s Schedules) RunJanitor(ctx context.Context, p project.Project, req JanitorRun, now time.Time) ([]JanitorStart, error) {
	actor, err := s.Actions.work.MayOpenWork(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if req.Again && req.Queue == "" {
		return nil, fault.New(fault.Invalid, "name the queue to try again")
	}
	if !p.Purpose.Schedules() {
		return []JanitorStart{{Refused: "an eval copy runs no schedule"}}, nil
	}
	carried, err := s.carriedBy(ctx, p.ID, true)
	if err != nil {
		return nil, err
	}
	out := []JanitorStart{}
	var starts []firing
	for _, one := range carried {
		if one.err != nil {
			return nil, one.err
		}
		if one.standing == nil || one.trigger.Queue == "" || (req.Queue != "" && one.trigger.Queue != req.Queue) {
			continue
		}
		c := firing{carried: one, projectID: p.ID, slug: p.Slug}
		result := JanitorStart{Queue: c.trigger.Queue, Workflow: c.workflow}
		eligible, err := s.resolve(ctx, &c)
		if err != nil {
			return nil, err
		}
		if !eligible {
			result.Refused = c.waiting
			out = append(out, result)
			continue
		}
		starts = append(starts, c)
	}
	if req.Again && len(starts) > 0 {
		if err := s.Actions.work.TryQueueAgain(ctx, p.ID, req.Queue, req.ChangeID); err != nil {
			return nil, err
		}
	}
	for _, c := range starts {
		result := JanitorStart{Queue: c.trigger.Queue, Workflow: c.workflow}

		got, err := s.admit(ctx, c)
		if err != nil {
			return nil, err
		}
		if got.outcome == fireNone {
			if c.publication {
				if s.Packs == nil {
					return nil, fmt.Errorf("pack catch-up is unavailable")
				}
				result.WhatIfID, err = s.Packs.CatchUpProject(ctx, p.ID)
				if err != nil && fault.KindOf(err) != fault.Unprocessable && fault.KindOf(err) != fault.Conflict {
					return nil, err
				}
				result.Current = err == nil && result.WhatIfID.IsZero()
				if !result.WhatIfID.IsZero() {
					if rerr := audit.At(now).Record(context.WithoutCancel(ctx), s.DB, actor, p.ID, "schedule.run-requested", c.workflow, map[string]any{"whatIfId": result.WhatIfID}); rerr != nil {
						err = fmt.Errorf("assessment %s started; recording the request: %w", result.WhatIfID, rerr)
					}
				}
			} else {
				got, err = s.runRequested(ctx, c, now, actor)
			}
		}

		if err != nil {
			result.Refused = err.Error()
		} else {
			result.Refused = got.refusal(c.trigger.Queue)
		}
		if got.change != nil {
			result.ChangeID, result.ChangeNumber = got.change.ID, got.change.Number
		}
		result.BusyChangeID, result.BusyChangeNumber = got.busy, got.busyNumber
		out = append(out, result)
	}
	if len(out) == 0 {
		out = append(out, JanitorStart{Queue: req.Queue, Refused: "No janitor schedule adopted"})
	}
	return out, nil
}
