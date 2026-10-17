package pi

import (
	"strings"
	"testing"
)

const usageLine = `{"type":"message_end","message":{"role":"assistant","model":"m","provider":"p","usage":{"input":10,"output":5,"totalTokens":15}}}`

// A transcript read whole is complete: every record ends in a newline and
// parses, and lines that are not JSON (a wrapper's own words on the shared
// output) are not records. A broken JSON line, or a last record cut off
// before its newline, may have held usage: the stream is then incomplete,
// and the reports are those of the records that did parse.
func TestReadUsageSaysWhetherTheTranscriptWasReadWhole(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, out  string
		reports    int
		incomplete string
	}{
		{name: "whole", out: usageLine + "\n" + `{"type":"turn_end"}` + "\n" + usageLine + "\n", reports: 2},
		{name: "empty", out: "", reports: 0},
		{name: "words that are not records", out: "npm warn deprecated\n" + usageLine + "\nexit status 0", reports: 1},
		{name: "a broken record among valid ones", out: usageLine + "\n" + `{"type":"message_end","message":{"usage":{"input":` + "\n" + usageLine + "\n", reports: 2, incomplete: "did not parse"},
		{name: "a last record without its newline", out: usageLine + "\n" + usageLine[:40], reports: 1, incomplete: "no newline"},
		{name: "a whole last record without its newline", out: usageLine + "\n" + usageLine, reports: 2, incomplete: "no newline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := ReadUsage([]byte(tc.out))
			if len(s.Reports) != tc.reports {
				t.Fatalf("reports: %d, want %d", len(s.Reports), tc.reports)
			}
			for _, r := range s.Reports {
				if r.Usage.Total != 15 || r.Model != "m" || r.Provider != "p" {
					t.Fatalf("report: %+v", r)
				}
			}
			if tc.incomplete == "" {
				if s.Incomplete != "" {
					t.Fatalf("a whole transcript read as incomplete: %q", s.Incomplete)
				}
				return
			}
			if s.Incomplete == "" || !strings.Contains(s.Incomplete, tc.incomplete) {
				t.Fatalf("incomplete: %q, want it to say %q", s.Incomplete, tc.incomplete)
			}
		})
	}
}
