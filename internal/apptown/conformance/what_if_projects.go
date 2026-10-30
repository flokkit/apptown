package conformance

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/flokkit/apptown/internal/ids"
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
	StatusLabel      CheckLabel `json:"statusLabel"`
	ProjectID        ids.ID     `json:"projectId"`
	ProjectSlug      string     `json:"projectSlug"`
	SHA              string     `json:"sha,omitempty"`
	AdoptionRevision int64      `json:"adoptionRevision,omitempty"`
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
	// Score counts the project's checks by what the what-if could say of
	// them; the outcome above is its worst case, the score the whole of it.
	Score WhatIfScore `json:"score"`
	// NotAssessedBy groups the checks the version carries and could not
	// judge by why, with what would make them assessable, in
	// NotAssessedReasons order.
	NotAssessedBy []WhatIfReasonCount `json:"notAssessedBy"`
	// Moves is every rule whose version taking the version changes; those
	// that go to an older version or out of force move it back. They sit
	// beside the outcome, never folded into it.
	Moves []WhatIfMove `json:"moves"`
}

// WhatIfScore counts one project's checks by the version's side:
// Checks = Judged + CouldNotAssess + NoLongerRequired, and Judged = Passed +
// Failing. NewFailures and Fixed compare with today. MovesBack and
// RuleChanges count rules, not checks.
type WhatIfScore struct {
	Checks           int `json:"checks"`
	Judged           int `json:"judged"`
	Passed           int `json:"passed"`
	Failing          int `json:"failing"`
	CouldNotAssess   int `json:"couldNotAssess"`
	NoLongerRequired int `json:"noLongerRequired"`
	// NotApplicable: checks the version declares that apply to nothing
	// this project declares; neither judged nor unjudgeable.
	NotApplicable int `json:"notApplicable"`
	NewFailures   int `json:"newFailures"`
	Fixed         int `json:"fixed"`
	MovesBack     int `json:"movesBack"`
	RuleChanges   int `json:"ruleChanges"`
}

// WhatIfReasonCount is how many checks one reason left unjudged, and what
// would make them assessable.
type WhatIfReasonCount struct {
	Reason NotAssessed `json:"reason"`
	Words  string      `json:"words"`
	Remedy string      `json:"remedy"`
	Count  int         `json:"count"`
}

// WhatIfMove is a rule whose version taking the version changes: From is
// zero when it is newly in force, To when it is no longer in force. Back
// says it goes to an older version or out of force.
type WhatIfMove struct {
	Rule string `json:"rule"`
	From int    `json:"from"`
	To   int    `json:"to"`
	Back bool   `json:"back"`
}

// WhatIfFleetGap is one check one reason left unjudged, across the
// projects of a what-if: a check that can never be judged stands out by
// the number of projects it is missing from.
type WhatIfFleetGap struct {
	Block    string      `json:"block"`
	Check    string      `json:"check"`
	Reason   NotAssessed `json:"reason"`
	Words    string      `json:"words"`
	Remedy   string      `json:"remedy"`
	Projects int         `json:"projects"`
}

// WhatIfFleetGaps groups, across projects, the checks the version carries
// and could not judge, by check and reason, most projects first. A
// whole-project row (no main, not adoptable, nothing to judge) is its own
// entry with no block.
func WhatIfFleetGaps(rows []WhatIfRow) []WhatIfFleetGap {
	type key struct {
		block, check string
		reason       NotAssessed
	}
	seen := map[key]map[ids.ID]bool{}
	for _, r := range rows {
		whole := r.Block == "" && r.Result == WhatIfNotAssessed
		if !whole && !versionSideUnjudged(r) {
			continue
		}
		k := key{r.Block, r.Check, r.Reason}
		if seen[k] == nil {
			seen[k] = map[ids.ID]bool{}
		}
		seen[k][r.ProjectID] = true
	}
	out := make([]WhatIfFleetGap, 0, len(seen))
	for k, projects := range seen {
		out = append(out, WhatIfFleetGap{Block: k.block, Check: k.check, Reason: k.reason, Words: k.reason.Words(), Remedy: k.reason.Remedy(), Projects: len(projects)})
	}
	slices.SortFunc(out, func(a, b WhatIfFleetGap) int {
		return cmp.Or(cmp.Compare(b.Projects, a.Projects), cmp.Compare(a.Block, b.Block), cmp.Compare(a.Check, b.Check), cmp.Compare(a.Reason, b.Reason))
	})
	return out
}

// WhatIfMovesBackProjects counts the projects the version moves a rule
// back on.
func WhatIfMovesBackProjects(projects []WhatIfProject) int {
	n := 0
	for _, p := range projects {
		if p.Score.MovesBack > 0 {
			n++
		}
	}
	return n
}

func judgedOutcome(state string) bool {
	return state == Conforms.String() || state == DoesNotConform.String() || state == Excepted.String()
}

// checkRow: a row that answers one check (not a rule move, not the whole
// project).
func checkRow(r WhatIfRow) bool { return r.Block != "" && r.Check != "" && !r.RuleMove() }

// versionSideUnjudged: a check the version carries and could not judge.
func versionSideUnjudged(r WhatIfRow) bool {
	return checkRow(r) && r.Proposed != "" && !judgedOutcome(r.Proposed)
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
		p := &WhatIfProject{ProjectID: id, ProjectSlug: slug, Outcome: pending, NewFailures: []WhatIfCheck{}, NotAssessed: []WhatIfCheck{}, NotAssessedBy: []WhatIfReasonCount{}, Moves: []WhatIfMove{}}
		byID[id], order = p, append(order, id)
		return p
	}
	for _, id := range selected {
		add(id, "")
	}
	answered := map[ids.ID]bool{}
	reasons := map[ids.ID]map[NotAssessed]int{}
	for _, r := range rows {
		p := add(r.ProjectID, r.ProjectSlug)
		p.ProjectSlug, p.SHA, p.AdoptionRevision, p.Joins = r.ProjectSlug, r.SHA, r.AdoptionRevision, r.Joins
		if r.CandidateDigest != "" {
			p.CandidateDigest = r.CandidateDigest
		}
		answered[r.ProjectID] = true
		if r.RuleMove() {
			p.Moves = append(p.Moves, WhatIfMove{Rule: r.Block, From: r.CurrentVersion, To: r.ProposedVersion, Back: r.MovesBack()})
			p.Score.RuleChanges++
			if r.MovesBack() {
				p.Score.MovesBack++
			}
			continue
		}
		if checkRow(r) {
			p.Score.Checks++
			switch {
			case r.Proposed == "":
				p.Score.NoLongerRequired++
			case r.Proposed == NotApplicable.String():
				p.Score.NotApplicable++
			case r.Proposed == Conforms.String():
				p.Score.Judged++
				p.Score.Passed++
			case judgedOutcome(r.Proposed):
				p.Score.Judged++
				p.Score.Failing++
			default:
				p.Score.CouldNotAssess++
				if reasons[r.ProjectID] == nil {
					reasons[r.ProjectID] = map[NotAssessed]int{}
				}
				reasons[r.ProjectID][r.Reason]++
			}
		}
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
		p.Score.NewFailures, p.Score.Fixed = len(p.NewFailures), p.Fixed
		for _, reason := range NotAssessedReasons {
			if n := reasons[id][reason]; n > 0 {
				p.NotAssessedBy = append(p.NotAssessedBy, WhatIfReasonCount{Reason: reason, Words: reason.Words(), Remedy: reason.Remedy(), Count: n})
				delete(reasons[id], reason)
			}
		}
		// A reason outside the known set (an older row) is still counted.
		for _, reason := range slices.Sorted(maps.Keys(reasons[id])) {
			p.NotAssessedBy = append(p.NotAssessedBy, WhatIfReasonCount{Reason: reason, Words: reason.Words(), Remedy: reason.Remedy(), Count: reasons[id][reason]})
		}
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
