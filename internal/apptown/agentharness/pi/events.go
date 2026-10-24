package pi

import (
	"bytes"
	"strings"
)

// CaptureEvents is the end of a script (run under set -e) that runs "$@",
// pi, with its event stream written to the file events as it goes, and
// passed on to stdout, where only the live feed reads it. The result is
// read from the file: a container's stdout can be lost at its end, and an
// event stream without its end reads as an agent that said nothing.
//
// The script exits with pi's own status, kept in the file status, which
// errexit cannot cut short; a tee that fails ends the script with its own.
func CaptureEvents(events, status string) string {
	return `{ st=0; "$@" || st=$?; echo "$st" >` + quote(status) + `; } | tee ` + quote(events) + "\n" +
		`exit "$(cat ` + quote(status) + `)"` + "\n"
}

// CapturedArgv is argv run with its events written to events (and its
// status to status), relative to the directory it runs in.
func CapturedArgv(argv []string, events, status string) []string {
	return append([]string{"sh", "-c", "set -e\n" + CaptureEvents(events, status), "sh"}, argv...)
}

// IsEvent says whether a line of pi's output is one of its JSON events
// (stderr and other noise are not).
func IsEvent(line []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(line), []byte("{"))
}

// EventsAfter is out with its first n event lines, and every line that is
// not an event, left out: what the live feed, which counted n events of the
// log, has not yet shown.
func EventsAfter(out []byte, n int64) []byte {
	var rest []byte
	for _, line := range bytes.SplitAfter(out, []byte("\n")) {
		if !IsEvent(line) {
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
