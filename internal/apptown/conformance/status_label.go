package conformance

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

type WhatIfLabel uint8

const (
	LabelUnknown  WhatIfLabel = 1
	LabelWaiting  WhatIfLabel = 2
	LabelWorking  WhatIfLabel = 3
	LabelFinished WhatIfLabel = 4
	LabelFailed   WhatIfLabel = 5
	LabelStopped  WhatIfLabel = 6
)

var labelCodec = enum.Set[WhatIfLabel]{Names: []string{"unknown", "waiting", "working", "finished", "failed", "stopped"}, Invalid: "invalid-status-label", Err: errors.New("invalid status label"), ScanLabel: "value"}

func (s WhatIfLabel) String() string                { return labelCodec.String(s) }
func (s WhatIfLabel) Value() (driver.Value, error)  { return labelCodec.Value(s) }
func (s WhatIfLabel) MarshalJSON() ([]byte, error)  { return labelCodec.JSON(s) }
func (s *WhatIfLabel) UnmarshalJSON(b []byte) error { return labelCodec.ReadJSON(s, b) }

func outcomeLabel(s string) CheckLabel {
	switch s {
	case WhatIfNewFailures:
		return CheckFailed
	case WhatIfNoNewFailures:
		return CheckPassed
	default:
		return CheckUnknown
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
	out.StatusLabel = LabelUnknown
	switch w.State {
	case "queued":
		out.StatusLabel = LabelWaiting
	case "running":
		out.StatusLabel = LabelWorking
	case "done":
		out.StatusLabel = LabelFinished
	case "error":
		out.StatusLabel = LabelFailed
	case "interrupted":
		out.StatusLabel = LabelStopped
	}
	out.OutcomeLabels = map[string]CheckLabel{}
	for _, s := range []string{WhatIfNewFailures, WhatIfCouldNotAssess, WhatIfNoNewFailures, WhatIfNotEvaluated, WhatIfNotYetJudged} {
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
	out.StatusLabel = CheckUnknown
	switch r.Result {
	case WhatIfUnchanged:
		if outcome, err := ParseOutcome(r.Proposed); err == nil {
			out.StatusLabel = outcome.Label()
		}
	case WhatIfWouldFail:
		out.StatusLabel = CheckFailed
	case WhatIfWouldPass:
		out.StatusLabel = CheckPassed
	case WhatIfNotApplicable, WhatIfNoLongerNeeded:
		out.StatusLabel = CheckNotApplicable
	}
	return json.Marshal(out)
}
