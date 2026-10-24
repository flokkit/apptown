// Package pitest helps a test's stand-in for pi answer as pi run through
// its capture (pi.CapturedArgv) does.
package pitest

import "strings"

// CapturedEvents is the events file a CapturedArgv names, and the argv it
// runs; ok is false for an argv that is not one. A stand-in for pi writes
// its stream there, as the real one's tee does.
func CapturedEvents(argv []string) (events string, inner []string, ok bool) {
	if len(argv) < 4 || argv[0] != "sh" || argv[1] != "-c" || argv[3] != "sh" || !strings.HasPrefix(argv[2], "set -e\n{ st=0; \"$@\"") {
		return "", nil, false
	}
	_, rest, found := strings.Cut(argv[2], "| tee '")
	if !found {
		return "", nil, false
	}
	events, _, _ = strings.Cut(rest, "'")
	return events, argv[4:], true
}
