package docker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A container's recent log, read on an explicit request: the newest lines,
// bounded in count, size and time. There is no follow stream. A log may hold
// an application's secrets; the caller decides who may read it and keeps it
// out of every record.
const (
	LogMaxLines   = 200
	LogMaxBytes   = 64 << 10
	logReadWithin = 10 * time.Second
)

// recentLog is what one read of a container's log returned: its newest
// lines, oldest first. Truncated says older output may exist that is not
// shown: the read reached its line bound, or was cut to its size bound.
type recentLog struct {
	Text      string
	Truncated bool
}

// ErrLogUnreadable is a log the host did not give: the daemon refused, the
// read timed out or was cancelled. Its words never include what the Docker
// client wrote, which may name a host's address or credentials.
var ErrLogUnreadable = errors.New("the host did not give the container's log")

// ReadRecentLog reads the newest LogMaxLines lines of a container's output
// (stdout and stderr, with Docker's timestamps), cut to the newest
// LogMaxBytes, within LogReadWithin. A container that is absent is
// ErrContainerAbsent; any other failure is ErrLogUnreadable with only a
// timeout or cancellation said.
func ReadRecentLog(ctx context.Context, dk Scope, container string) (recentLog, error) {
	out, err := dk.Command(ctx, Command{Args: []string{"logs", "--tail", strconv.Itoa(LogMaxLines), "--timestamps", container}, Timeout: logReadWithin})
	switch {
	case ctx.Err() != nil:
		return recentLog{}, fmt.Errorf("%w: the read was cancelled", ErrLogUnreadable)
	case err != nil && absentIn(out, container):
		return recentLog{}, fmt.Errorf("%w: %s", ErrContainerAbsent, container)
	case err != nil && errors.Is(err, context.DeadlineExceeded):
		return recentLog{}, fmt.Errorf("%w: it did not answer within %s", ErrLogUnreadable, logReadWithin)
	case err != nil:
		return recentLog{}, ErrLogUnreadable
	}
	return boundLog(out), nil
}

// boundLog keeps the newest LogMaxLines lines and, of those, the newest
// LogMaxBytes, starting at a whole line where one fits.
func boundLog(out []byte) recentLog {
	text := strings.TrimRight(string(out), "\n")
	lines := strings.Split(text, "\n")
	cut := len(lines) >= LogMaxLines
	if len(lines) > LogMaxLines {
		text = strings.Join(lines[len(lines)-LogMaxLines:], "\n")
	}
	if len(text) > LogMaxBytes {
		text, cut = text[len(text)-LogMaxBytes:], true
		if i := strings.IndexByte(text, '\n'); i >= 0 && i < len(text)-1 {
			text = text[i+1:]
		}
	}
	return recentLog{Text: text, Truncated: cut}
}

// absentIn says whether a failed command's output is the daemon saying it
// has no such container (decodeRuntime's rule).
func absentIn(out []byte, name string) bool {
	_, err := decodeRuntime(out, errors.New("failed"), name)
	return errors.Is(err, ErrContainerAbsent)
}
