package workflowexec

import (
	"testing"
)

func TestSumPiUsage(t *testing.T) {
	t.Parallel()
	stream := `{"type":"message_end","message":{"role":"assistant","model":"m1","provider":"p1","usage":{"input":100,"output":20,"cacheRead":500,"cacheWrite":0,"totalTokens":620,"cost":{"total":0.0015}}}}
{"type":"turn_end"}
{"type":"message_end","message":{"role":"assistant","model":"m1","provider":"p1","usage":{"input":200,"output":30,"cacheRead":0,"cacheWrite":0,"totalTokens":230,"cost":{"total":0.002}}}}
`
	spent := sumPiUsage([]byte(stream))
	u := spent.Totals
	if u.Input != 300 || u.Output != 50 || u.CacheRead != 500 || u.Total != 850 {
		t.Errorf("sum wrong: %+v", u)
	}
	if u.CostMicros != 3500 {
		t.Errorf("cost: want 3500 micros, got %d", u.CostMicros)
	}
	if spent.Model != "m1" || spent.Provider != "p1" {
		t.Errorf("model attribution: %s/%s", spent.Model, spent.Provider)
	}
	if spent.Incomplete != "" {
		t.Errorf("a whole transcript is complete: %q", spent.Incomplete)
	}
}

// A usage record that does not parse is not silently dropped from a
// complete-looking total: the sum is of the records that did parse, and it
// is marked incomplete.
func TestSumPiUsageOfABrokenTranscriptIsIncomplete(t *testing.T) {
	t.Parallel()
	stream := `{"type":"message_end","message":{"role":"assistant","model":"m1","provider":"p1","usage":{"input":100,"output":20,"totalTokens":120}}}
{"type":"message_end","message":{"role":"assistant","usage":{"input":9000,"out
{"type":"message_end","message":{"role":"assistant","model":"m1","provider":"p1","usage":{"input":200,"output":30,"totalTokens":230}}}
`
	spent := sumPiUsage([]byte(stream))
	if spent.Totals.Total != 350 || spent.Incomplete == "" {
		t.Fatalf("want the valid records' 350, incomplete: %+v", spent)
	}
}
