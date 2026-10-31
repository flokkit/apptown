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
	Err:       ErrReviewState,
	ScanLabel: "database value",
}

var (
	ErrReviewClosed     = errors.New("review is missing or already finished")
	ErrReviewTransition = errors.New("invalid review transition")
	ErrReviewState      = errors.New("invalid review state")
)

func (s ReviewState) String() string { return reviewStateCodec.String(s) }

// RequireRunning is the admission rule for progress, verdicts and snapshots.
func (s ReviewState) RequireRunning() error {
	if s != ReviewRunning {
		return ErrReviewClosed
	}
	return nil
}

// Transition is the only rule for changing a persisted review state. Storage
// must apply it to the authoritative locked state and commit the returned state
// with the required receipts. It never mutates a detached caller's snapshot.
// A queued review is claimed (running) or ends unstarted (error); a running
// review ends done or error.
func (s ReviewState) Transition(next ReviewState) (ReviewState, error) {
	switch {
	case s == ReviewQueued && (next == ReviewRunning || next == ReviewError):
		return next, nil
	case s == ReviewRunning && (next == ReviewDone || next == ReviewError):
		return next, nil
	case s != ReviewQueued && s != ReviewRunning:
		return 0, ErrReviewClosed
	default:
		return 0, fmt.Errorf("%w: %s to %s", ErrReviewTransition, s, next)
	}
}

func ParseReviewState(text string) (ReviewState, error) { return reviewStateCodec.Parse(text) }

func (s ReviewState) Value() (driver.Value, error) { return reviewStateCodec.Value(s) }

func (s *ReviewState) Scan(value any) error { return reviewStateCodec.Scan(s, value) }

func (s ReviewState) MarshalJSON() ([]byte, error) { return reviewStateCodec.JSON(s) }

func (s *ReviewState) UnmarshalJSON(data []byte) error {
	return reviewStateCodec.ReadJSON(s, data)
}

// Display describes review work without claiming that its checks passed.
func (s ReviewState) Display() WhatIfLabel {
	switch s {
	case ReviewQueued:
		return LabelWaiting
	case ReviewRunning:
		return LabelWorking
	case ReviewDone:
		return LabelFinished
	case ReviewError:
		return LabelFailed
	default:
		return LabelUnknown
	}
}
