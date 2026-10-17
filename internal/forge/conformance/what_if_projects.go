package conformance

import (
	"fmt"

	"flokkit.io/forge/internal/ids"
)

// A what-if's answer per project (design/subsystems/conformance.md): what
// taking the version would change for that project, worst first. It is
// read from the rows, never stored: the rows are the record.

// Project outcomes of a what-if, worst first.
const (
	// WhatIfNewFailures: a check would fail that does not fail today, or one
	// excepted today would fail because its exception does not cover the
	// version's check.
	WhatIfNewFailures = "new-failures"
	// WhatIfCouldNotAssess: something the version changes could not be
	// judged; the project is not shown as clear.
	WhatIfCouldNotAssess = "could-not-assess"
	// WhatIfNoNewFailures: everything the version changes was judged and
	// nothing new fails.
	WhatIfNoNewFailures = "no-new-failures"
	// WhatIfNotEvaluated: the what-if ended (stopped or interrupted)
	// before it reached the project.
	WhatIfNotEvaluated = "not-evaluated"
	// WhatIfNotYetJudged: the what-if is queued or running and has not
	// reached the project yet. It is not an answer and not an end.
	WhatIfNotYetJudged = "not-yet-judged"
)

// WhatIfCheck is one check in a project's answer.
type WhatIfCheck struct {
	Block string `json:"block"`
	Check string `json:"check"`
	// ExceptedToday: the check is excepted today and its exception does not
	// cover the version's check.
	ExceptedToday bool `json:"exceptedToday,omitempty"`
	// Reason is why a check was not judged, Why its words; empty for a new
	// failure.
	Reason NotAssessed `json:"reason,omitempty"`
	Why    string      `json:"why,omitempty"`
}

// WhatIfProject is one selected project's answer.
type WhatIfProject struct {
	ProjectID        ids.ID `json:"projectId"`
	ProjectSlug      string `json:"projectSlug"`
	SHA              string `json:"sha,omitempty"`
	AdoptionRevision int64  `json:"adoptionRevision,omitempty"`
	// CandidateDigest is the candidate assessed, which adopting from the
	// what-if must still match; empty when the project had none.
	CandidateDigest string `json:"candidateDigest,omitempty"`
	Joins           bool   `json:"joins,omitempty"`
	Outcome         string `json:"outcome"`
	// NewFailures and NotAssessed name the checks behind the outcome; a
	// whole-project row (no main, not adoptable) is one NotAssessed entry
	// with an empty Block.
	NewFailures []WhatIfCheck `json:"newFailures"`
	NotAssessed []WhatIfCheck `json:"notAssessed"`
	// Fixed counts the checks that fail today and would pass.
	Fixed int `json:"fixed"`
}

// WhatIfByProject is each selected project's answer, in selection order,
// then any project with rows the selection does not name (a what-if over
// the whole estate stores no list). slugs names the selected projects
// without rows; one it lacks reads "project #ID". ended says the what-if
// has stopped: a project without rows is then not evaluated, and before it
// is not yet judged.
func WhatIfByProject(selected []ids.ID, rows []WhatIfRow, slugs map[ids.ID]string, ended bool) []WhatIfProject {
	pending := WhatIfNotYetJudged
	if ended {
		pending = WhatIfNotEvaluated
	}
	byID := map[ids.ID]*WhatIfProject{}
	order := []ids.ID{}
	add := func(id ids.ID, slug string) *WhatIfProject {
		if p, ok := byID[id]; ok {
			return p
		}
		if slug == "" {
			slug = slugs[id]
		}
		if slug == "" {
			slug = fmt.Sprintf("project #%d", id)
		}
		p := &WhatIfProject{ProjectID: id, ProjectSlug: slug, Outcome: pending, NewFailures: []WhatIfCheck{}, NotAssessed: []WhatIfCheck{}}
		byID[id], order = p, append(order, id)
		return p
	}
	for _, id := range selected {
		add(id, "")
	}
	answered := map[ids.ID]bool{}
	for _, r := range rows {
		p := add(r.ProjectID, r.ProjectSlug)
		p.ProjectSlug, p.SHA, p.AdoptionRevision, p.Joins = r.ProjectSlug, r.SHA, r.AdoptionRevision, r.Joins
		if r.CandidateDigest != "" {
			p.CandidateDigest = r.CandidateDigest
		}
		answered[r.ProjectID] = true
		c := WhatIfCheck{Block: r.Block, Check: r.Check}
		switch {
		case r.Result == WhatIfWouldFail:
			p.NewFailures = append(p.NewFailures, c)
		case exceptionLapses(r):
			c.ExceptedToday = true
			p.NewFailures = append(p.NewFailures, c)
		case r.Result == WhatIfNotAssessed && !sameEvaluation(r):
			c.Reason, c.Why = r.Reason, r.Reason.Words()
			p.NotAssessed = append(p.NotAssessed, c)
		case r.Result == WhatIfWouldPass:
			p.Fixed++
		}
	}
	out := make([]WhatIfProject, 0, len(order))
	for _, id := range order {
		p := byID[id]
		switch {
		case !answered[id]:
		case len(p.NewFailures) > 0:
			p.Outcome = WhatIfNewFailures
		case len(p.NotAssessed) > 0:
			p.Outcome = WhatIfCouldNotAssess
		default:
			p.Outcome = WhatIfNoNewFailures
		}
		out = append(out, *p)
	}
	return out
}

// exceptionLapses: excepted today and failing under the version. Both
// sides keep the exception matcher's answer, so this is the transition
// adoption would make, not a guess from block versions: a whole-check
// exception stops covering a moved block, an identifier exception does not.
func exceptionLapses(r WhatIfRow) bool {
	return r.Block != "" && r.Current == Excepted.String() && r.Proposed == DoesNotConform.String()
}

// sameEvaluation: a check not judged that both sides carry at the same
// block version is one evaluation answering both, so taking the version
// cannot change it. It is not part of what the version changes; the
// project's standing still shows it.
func sameEvaluation(r WhatIfRow) bool {
	return r.Block != "" && r.CurrentVersion != 0 && r.CurrentVersion == r.ProposedVersion && r.Current == r.Proposed
}

// WhatIfOutcomeWords says a project's outcome the way every surface says it.
func WhatIfOutcomeWords(outcome string) string {
	switch outcome {
	case WhatIfNewFailures:
		return "new failures"
	case WhatIfCouldNotAssess:
		return "could not assess"
	case WhatIfNoNewFailures:
		return "no new failures"
	case WhatIfNotEvaluated:
		return "not evaluated"
	case WhatIfNotYetJudged:
		return "not yet judged"
	}
	return outcome
}
