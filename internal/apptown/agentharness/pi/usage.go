package pi

import (
	"bytes"
	"encoding/json"

	"github.com/flokkit/apptown/internal/console"
)

type usageReport struct {
	Model, Provider string
	Usage           console.Usage
}

// usageStream is the usage a Pi JSON-mode transcript reports, and whether
// those reports can be every one the transcript held. Pi writes only JSON
// lines, each ending in a newline, so a JSON line that does not parse, or a
// last record without its newline, may have held usage that is now lost:
// the totals of the reports that survived are then a lower bound, never a
// complete answer.
type usageStream struct {
	Reports []usageReport
	// Incomplete says why the reports may not be all the transcript held;
	// "" when the transcript was read whole.
	Incomplete string
}

// ReadUsage reads every assistant message_end usage report of a whole
// transcript. The caller joins the transcript's bytes before calling, so a
// record split across reads is one line here. Lines that are not JSON (a
// wrapper's own words on the shared output) are not records and are
// skipped; a broken JSON line or a partial last record makes the stream
// incomplete.
func ReadUsage(out []byte) usageStream {
	var s usageStream
	lines := bytes.Split(out, []byte("\n"))
	for i, raw := range lines {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if i == len(lines)-1 && s.Incomplete == "" {
			// bytes.Split leaves a last element only when the output did
			// not end in a newline: the record was cut off.
			s.Incomplete = "the last record has no newline (a partial record)"
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Role     string        `json:"role"`
				Model    string        `json:"model"`
				Provider string        `json:"provider"`
				Usage    console.Usage `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			if s.Incomplete == "" {
				s.Incomplete = "a record did not parse: " + err.Error()
			}
			continue
		}
		if ev.Type == "message_end" && ev.Message.Role == "assistant" {
			s.Reports = append(s.Reports, usageReport{Model: ev.Message.Model, Provider: ev.Message.Provider, Usage: ev.Message.Usage})
		}
	}
	return s
}
