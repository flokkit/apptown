package conformance

import (
	"database/sql/driver"
	"encoding/json"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

type WhatIfLabel uint8

const (
	labelUnknown  WhatIfLabel = 1
	labelWaiting  WhatIfLabel = 2
	labelWorking  WhatIfLabel = 3
	labelFinished WhatIfLabel = 4
	labelFailed   WhatIfLabel = 5
	labelStopped  WhatIfLabel = 6
)

var labelCodec = enum.Set[WhatIfLabel]{Names: []string{"unknown", "waiting", "working", "finished", "failed", "stopped"}, Invalid: "invalid-status-label", Err: errors.New("invalid status label"), ScanLabel: "value"}

func (s WhatIfLabel) String() string                { return labelCodec.String(s) }
func (s WhatIfLabel) Value() (driver.Value, error)  { return labelCodec.Value(s) }
func (s WhatIfLabel) MarshalJSON() ([]byte, error)  { return labelCodec.JSON(s) }
func (s *WhatIfLabel) UnmarshalJSON(b []byte) error { return labelCodec.ReadJSON(s, b) }

func outcomeLabel(s string) CheckLabel {
	switch s {
	case WhatIfNewFailures:
		return checkFailed
	case WhatIfNoNewFailures:
		return checkPassed
	default:
		return checkUnknown
	}
}
func (w WhatIf) MarshalJSON() ([]byte, error) {
	type view WhatIf
	out := view(w)
	if out.Also == nil {
		out.Also = []PackVersion{}
	}
	if out.Rows == nil {
		out.Rows = []WhatIfRow{}
	}
	if out.Projects == nil {
		out.Projects = []WhatIfProject{}
	}
	if out.Fleet == nil {
		out.Fleet = []WhatIfFleetGap{}
	}
	out.StatusLabel = labelUnknown
	switch w.State {
	case "queued":
		out.StatusLabel = labelWaiting
	case "running":
		out.StatusLabel = labelWorking
	case "done":
		out.StatusLabel = labelFinished
	case "error":
		out.StatusLabel = labelFailed
	case "interrupted":
		out.StatusLabel = labelStopped
	}
	out.OutcomeLabels = map[string]CheckLabel{}
	for _, s := range []string{WhatIfNewFailures, whatIfCouldNotAssess, WhatIfNoNewFailures, whatIfNotEvaluated, whatIfNotYetJudged} {
		out.OutcomeLabels[s] = outcomeLabel(s)
	}
	return json.Marshal(out)
}
func (w WhatIfProject) MarshalJSON() ([]byte, error) {
	type view WhatIfProject
	out := view(w)
	out.StatusLabel = outcomeLabel(w.Outcome)
	if out.NewFailures == nil {
		out.NewFailures = []WhatIfCheck{}
	}
	if out.NotAssessed == nil {
		out.NotAssessed = []WhatIfCheck{}
	}
	if out.NotAssessedBy == nil {
		out.NotAssessedBy = []WhatIfReasonCount{}
	}
	if out.Moves == nil {
		out.Moves = []WhatIfMove{}
	}
	return json.Marshal(out)
}
func (r WhatIfRow) MarshalJSON() ([]byte, error) {
	type view WhatIfRow
	out := view(r)
	out.StatusLabel = checkUnknown
	switch r.Result {
	case WhatIfUnchanged:
		if outcome, err := ParseOutcome(r.Proposed); err == nil {
			out.StatusLabel = outcome.label()
		}
	case WhatIfWouldFail:
		out.StatusLabel = checkFailed
	case WhatIfWouldPass:
		out.StatusLabel = checkPassed
	case whatIfNotApplicable, whatIfNoLongerNeeded:
		out.StatusLabel = checkNotApplicable
	}
	return json.Marshal(out)
}
