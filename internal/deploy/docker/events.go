package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/dockerlabel"
)

// ContainerEvent is one thing the daemon reported happening to a
// container: Docker's own record, which AppTown reads and never infers.
type ContainerEvent struct {
	Container string    // the container's name
	Action    string    // "oom", "die" or "kill"
	At        time.Time // the daemon's time for it
	ExitCode  *int      // a die's exit code; nil otherwise
	// Deployment and Generation are the container's AppTown identity labels,
	// as the event carried them: "" for a container that has none (a
	// deployment's needs and other parts outlive its generations).
	Deployment, Generation string
}

// ContainerEvents is what the daemon reports of containers being killed
// out of memory, exiting or being killed by a signal between since and
// until: one bounded read of its event log (docker events --until
// returns). The daemon keeps only its recent events in memory to replay,
// so a window larger than that buffer loses the oldest, and nothing is
// replayed across a daemon restart: an empty answer is "none reported",
// not "none happened".
func ContainerEvents(ctx context.Context, dk Scope, since, until time.Time) ([]ContainerEvent, error) {
	out, err := dk.Run(ctx, "events",
		"--since", unixArg(since), "--until", unixArg(until),
		"--filter", "type=container",
		"--filter", "event=oom", "--filter", "event=die", "--filter", "event=kill",
		"--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker events: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return parseContainerEvents(out)
}

// parseContainerEvents reads docker events' JSON lines. A line that is
// not an event AppTown reads is an error, never skipped: a skipped
// event would read as one that did not happen.
func parseContainerEvents(out []byte) ([]ContainerEvent, error) {
	var events []ContainerEvent
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e struct {
			Action   string `json:"Action"`
			TimeNano int64  `json:"timeNano"`
			Actor    struct {
				Attributes map[string]string `json:"Attributes"`
			} `json:"Actor"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("docker events reported a line AppTown cannot read: %w", err)
		}
		name := e.Actor.Attributes["name"]
		if name == "" || e.TimeNano == 0 {
			return nil, fmt.Errorf("docker events reported %s with no container name or time", e.Action)
		}
		ev := ContainerEvent{Container: name, Action: e.Action, At: time.Unix(0, e.TimeNano).UTC(),
			Deployment: e.Actor.Attributes[dockerlabel.Deployment], Generation: e.Actor.Attributes[dockerlabel.Generation]}
		if e.Action == "die" {
			if code, err := strconv.Atoi(e.Actor.Attributes["exitCode"]); err == nil {
				ev.ExitCode = &code
			}
		}
		events = append(events, ev)
	}
	return events, nil
}

func unixArg(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10) + "." + fmt.Sprintf("%09d", t.Nanosecond())
}
