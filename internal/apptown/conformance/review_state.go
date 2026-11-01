// Package conformance owns review lifecycle and assessment semantics. Persisted
// lifecycle states are distinct from evaluation outcomes and display overlays.
package conformance

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/enum"
)

// ReviewState is the lifecycle of one review. Its zero value is not a saved
// state. A queued review waits for the conformance queue's worker; a review
// leaves queued once. Ending a review is permanent; rerunning creates a new
// review identity.
type ReviewState uint8

const (
	ReviewRunning ReviewState = iota + 1
	ReviewDone
	ReviewError
	ReviewQueued
)

// reviewStateCodec spells each ReviewState in storage and JSON.
var reviewStateCodec = enum.Set[ReviewState]{
	Names:     []string{"running", "done", "error", "queued"},
	Invalid:   "invalid-review-state",
	Err:       errReviewState,
	ScanLabel: "database value",
}

var (
	errReviewClosed     = errors.New("review is missing or already finished")
	errReviewTransition = errors.New("invalid review transition")
	errReviewState      = errors.New("invalid review state")
)

func (s ReviewState) String() string { return reviewStateCodec.String(s) }

// requireRunning is the admission rule for progress, verdicts and snapshots.
func (s ReviewState) requireRunning() error {
	if s != ReviewRunning {
		return errReviewClosed
	}
	return nil
}

// transition is the only rule for changing a persisted review state. Storage
// must apply it to the authoritative locked state and commit the returned state
// with the required receipts. It never mutates a detached caller's snapshot.
// A queued review is claimed (running) or ends unstarted (error); a running
// review ends done or error.
func (s ReviewState) transition(next ReviewState) (ReviewState, error) {
	switch {
	case s == ReviewQueued && (next == ReviewRunning || next == ReviewError):
		return next, nil
	case s == ReviewRunning && (next == ReviewDone || next == ReviewError):
		return next, nil
	case s != ReviewQueued && s != ReviewRunning:
		return 0, errReviewClosed
	default:
		return 0, fmt.Errorf("%w: %s to %s", errReviewTransition, s, next)
	}
}

func parseReviewState(text string) (ReviewState, error) { return reviewStateCodec.Parse(text) }

func (s ReviewState) Value() (driver.Value, error) { return reviewStateCodec.Value(s) }

func (s *ReviewState) Scan(value any) error { return reviewStateCodec.Scan(s, value) }

func (s ReviewState) MarshalJSON() ([]byte, error) { return reviewStateCodec.JSON(s) }

func (s *ReviewState) UnmarshalJSON(data []byte) error {
	return reviewStateCodec.ReadJSON(s, data)
}

// display describes review work without claiming that its checks passed.
func (s ReviewState) display() WhatIfLabel {
	switch s {
	case ReviewQueued:
		return labelWaiting
	case ReviewRunning:
		return labelWorking
	case ReviewDone:
		return labelFinished
	case ReviewError:
		return labelFailed
	default:
		return labelUnknown
	}
}
