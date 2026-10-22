package conformance

import (
	"sort"

	"github.com/flokkit/apptown/internal/ids"
)

// ReviewDelta compares only stable project identities present in both reviews.
type ReviewDelta struct {
	Previous         ids.ID   `json:"previous"`
	ComparedProjects int      `json:"comparedProjects"`
	Fixed            int      `json:"fixed"`
	New              int      `json:"new"`
	StillOpen        int      `json:"stillOpen"`
	NewlyJudged      int      `json:"newlyJudged"`
	NewlyUnjudged    int      `json:"newlyUnjudged"`
	FixedItems       []string `json:"fixedItems,omitempty"`
	NewItems         []string `json:"newItems,omitempty"`
}

func isOpen(state Outcome) bool { return state == DoesNotConform }

func isSettled(state Outcome) bool {
	return state == Conforms || state == Excepted
}

func isUnjudged(state Outcome) bool {
	return state == CannotAssess || state == EvaluationError
}

// CompareReviews never treats a renamed project or a different check version as
// the same evaluated subject merely because its display text matches.
func CompareReviews(previousID ids.ID, cur, prev []Verdict) *ReviewDelta {
	previousProjects := map[ids.ID]bool{}
	prevBy := map[CheckIdentity]Outcome{}
	for _, v := range prev {
		previousProjects[v.ProjectID] = true
		prevBy[v.Identity()] = v.State
	}
	compared := map[ids.ID]bool{}
	d := &ReviewDelta{Previous: previousID}
	for _, v := range cur {
		if !previousProjects[v.ProjectID] {
			continue
		}
		compared[v.ProjectID] = true
		k := v.Identity()
		p, had := prevBy[k]
		label := v.ProjectSlug + " · " + v.Check
		switch {
		case had && isOpen(p) && isSettled(v.State):
			d.Fixed++
			d.FixedItems = append(d.FixedItems, label)
		case isOpen(v.State) && (!had || !isOpen(p)):
			d.New++
			d.NewItems = append(d.NewItems, label)
		case had && isOpen(p) && isOpen(v.State):
			d.StillOpen++
		}
		if had && isUnjudged(p) && !isUnjudged(v.State) && v.State != NotApplicable {
			d.NewlyJudged++
		}
		if had && !isUnjudged(p) && p != NotApplicable && isUnjudged(v.State) {
			d.NewlyUnjudged++
		}
	}
	d.ComparedProjects = len(compared)
	sort.Strings(d.FixedItems)
	sort.Strings(d.NewItems)
	return d
}
