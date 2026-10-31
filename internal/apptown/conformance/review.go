package conformance

import "github.com/flokkit/apptown/internal/ids"

import "github.com/flokkit/apptown/internal/stamp"

// Review is the immutable, read-only record of one estate sweep (§3.1).
// The next review is a new record and never overwrites this one.
type Review struct {
	ID      ids.ID `json:"id"`
	Number  int64  `json:"number"` // within AppTown, as people name it ("review 9")
	Trigger string `json:"trigger"`
	Actor   string `json:"actor"`
	// ActorPrincipal is the starter's principal; zero for AppTown, or one
	// not resolved to a person.
	ActorPrincipal ids.ID     `json:"actorPrincipal,omitzero"`
	StartedAt      string     `json:"startedAt"`
	FinishedAt     stamp.Time `json:"finishedAt"`
	Projects       int        `json:"projects"`
	Verdicts       int        `json:"verdicts"`
	Errors         int        `json:"errors"`
	Cost           float64    `json:"cost"` // model spend; 0 for a pure-posture (sh) review
	// Live run state (a review is observable, not a black box): status is
	// running until the walk finishes, and the counters advance per project so
	// the UI can show what it is doing.
	Status         ReviewState `json:"status"`
	ProjectsTotal  int         `json:"projectsTotal"`
	ProjectsDone   int         `json:"projectsDone"`
	CurrentProject string      `json:"currentProject"`
}

// ReviewSummary is one row of the Reviews page: the record plus what it found.
type ReviewSummary struct {
	Review
	StatusLabel          WhatIfLabel  `json:"statusLabel"`
	Findings             int          `json:"findings"`
	Flagged              int          `json:"flagged"`
	ProjectsWithFindings int          `json:"projectsWithFindings"`
	Judged               int          `json:"judged"`
	Total                int          `json:"total"`
	Delta                *ReviewDelta `json:"delta,omitempty"`
}
