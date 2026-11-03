package pi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed events.js
var eventFilter string

// CaptureEvents is the end of a script (run under set -e) that runs "$@",
// pi, with its event stream written to the file events as it goes, except
// tool_execution_update events: their cumulative output is superseded by
// tool_execution_end. Stdout keeps all events for the live feed. The result is
// read from the file: a container's stdout can be lost at its end, and an
// event stream without its end reads as an agent that said nothing.
//
// The script exits with pi's own status, kept in the file status, which
// errexit cannot cut short; a capture that fails ends the script with its own.
// Tool-result strings keep at most 16 KiB of their start and end, with a
// byte-counted cut notice. Assistant text stays whole. Node is Pi's runtime;
// this filter changes neither what the agent sees nor its session transcript.
func CaptureEvents(events, status string) string {
	return `{ st=0; "$@" || st=$?; echo "$st" >` + quote(status) + `; } | node -e ` + quote(eventFilter) + " " + quote(events) + "\n" +
		`exit "$(cat ` + quote(status) + `)"` + "\n"
}

// CapturedArgv is argv run with its events written to events (and its
// status to status), relative to the directory it runs in.
func CapturedArgv(argv []string, events, status string) []string {
	return append([]string{"sh", "-c", "set -e\n" + CaptureEvents(events, status), "sh"}, argv...)
}

// IsCapturedEvent says whether a line of pi's output counts toward the
// events file. Tool updates go only to stdout; counting them would skip
// later events when the live feed catches up from the file.
func IsCapturedEvent(line []byte) bool {
	if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
		return false
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		// The capture filter keeps a line it cannot parse, so it counts.
		return true
	}
	return event.Type != "tool_execution_update"
}

// EventsAfter is out with its first n event lines, and every line that is
// not an event, left out: what the live feed, which counted n events of the
// log, has not yet shown.
func EventsAfter(out []byte, n int64) []byte {
	var rest []byte
	for _, line := range bytes.SplitAfter(out, []byte("\n")) {
		if !IsCapturedEvent(line) {
			continue
		}
		if n > 0 {
			n--
			continue
		}
		rest = append(rest, line...)
	}
	return rest
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
