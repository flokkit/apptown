package httpapi

import (
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// RuntimeObservationView carries freshness without exposing provider errors.
type RuntimeObservationView struct {
	Observation            string `json:"observation"`
	ObservationIssue       string `json:"observationIssue,omitempty"`
	ObservedAt             string `json:"observedAt,omitempty"`
	ObservationAttemptedAt string `json:"observationAttemptedAt,omitempty"`
	ObservationStale       bool   `json:"observationStale"`
}

func runtimeObservationView(o deployment.RuntimeObservation) RuntimeObservationView {
	v := RuntimeObservationView{Observation: o.State.String(), ObservationStale: o.Stale}
	if !o.ObservedAt.IsZero() {
		v.ObservedAt = o.ObservedAt.UTC().Format(time.RFC3339Nano)
	}
	if !o.AttemptedAt.IsZero() {
		v.ObservationAttemptedAt = o.AttemptedAt.UTC().Format(time.RFC3339Nano)
	}
	if o.Err != nil {
		v.ObservationIssue = "Runtime status is unavailable"
	}
	return v
}
