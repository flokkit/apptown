package workflowexec

import (
	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"

	"github.com/flokkit/apptown/internal/apptown/usage"
)

// piUsage is one invocation's spending as its transcript reports it.
type piUsage struct {
	Totals          usage.Totals
	Model, Provider string // the last reported
	// Incomplete says why Totals may be lower than what was spent; "" when
	// the whole transcript was read.
	Incomplete string
}

// sumPiUsage aggregates every usage report in the harness output, retaining the
// last reported provider/model, and carries whether the output could be read
// whole. ParseOutput's last-message usage is insufficient for accounting across
// a whole execution. Costs are harness-reported estimates.
func sumPiUsage(out []byte) piUsage {
	stream := piharness.ReadUsage(out)
	r := piUsage{Incomplete: stream.Incomplete}
	for _, report := range stream.Reports {
		m := report.Usage
		r.Totals.Add(m.Input, m.Output, m.CacheRead, m.CacheWrite, m.Total, m.Cost.Total)
		if report.Model != "" {
			r.Model, r.Provider = report.Model, report.Provider
		}
	}
	return r
}
