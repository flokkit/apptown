// Package containerresult reads what a command in a container answers
// without relying on its standard output arriving. The Docker daemon can
// lose a short-lived container's stdout in the drain when it exits and
// still report exit 0 (seen in CI: a df that answered nothing, through
// docker run and through docker logs after docker wait alike), so nothing
// AppTown acts on is read from that stream.
//
// A small result (a value, a version, a list) is written to a file in the
// container's own layer and copied out with docker cp once the command
// has exited (Run, RunIn). A read that recurs runs in the daemon's
// standing reader of its kind, read with docker exec (RunStanding), so
// it starts no container of its own. A stream of data out of a container (a backup
// export) carries a trailer, its byte count and sha256, which the receiver
// holds it to (Framed, Receiver).
package containerresult

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/strs"
)

// Docker runs one docker CLI command on the daemon the result is read
// from, with env (NAME=value) added to the CLI's own environment, and
// returns what the CLI printed. A value reaches a container by name only
// (-e NAME), never as an argument another process can read.
type Docker func(ctx context.Context, env []string, args ...string) ([]byte, error)

// Spec is one command whose answer is read.
type Spec struct {
	// Image is the throwaway container's image (Run only).
	Image string
	// Options are docker create options (Run only): --network, -v,
	// --user and the like. The container runs sh -c with no entrypoint,
	// whatever the image declares.
	Options []string
	// Env names the variables the command is given, NAME=value; their
	// values travel in the CLI's environment.
	Env []string
	// Script is the sh script whose standard output is the answer. It is
	// written to a file in the container, never read from the stream.
	Script string
	// Max bounds the answer, in bytes; 0 is DefaultMax. A larger answer is
	// an error, never cut short.
	Max int64
	// Validate rejects an otherwise complete answer before its owned resources
	// are removed. Nil accepts any present answer, including an empty one.
	Validate func([]byte) error
	// Now stamps a throwaway container's deadline; nil is the real clock.
	Now func() time.Time
}

// DefaultMax bounds an answer whose Spec gives no bound.
const DefaultMax = 1 << 20

// Class is the apptown.class of a throwaway result container, and Prefix
// begins its name.
const (
	Class  = "container-result"
	Prefix = "apptown-result-"
)

// resultPath is where the answer is written in the container: /tmp in the
// container's own layer, which docker cp can read (it cannot read a tmpfs
// or a volume). The script writes beside it and renames, so a file there
// is a whole answer.
const resultPath = "/tmp/apptown-result"

// lifetime is how long a throwaway container may stand before another
// read's sweep removes it: far longer than a read takes, so a sweep never
// removes one still in use.
const lifetime = 10 * time.Minute

// removalBound bounds the removal of what a read made, which runs even
// when the caller has stopped.
const removalBound = time.Minute

// ErrNoResult is a command that exited 0 and left no answer: what it
// would have said is unknown.
var ErrNoResult = errors.New("the command left no answer")

// ExitError is a command that exited non-zero. Said is what the container
// printed, clipped, for an error's text only: it is never an answer.
type ExitError struct {
	Code int
	Said string
}

func (e *ExitError) Error() string {
	if e.Said == "" {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return fmt.Sprintf("exit status %d: %s", e.Code, e.Said)
}

// ExitCode is the command's exit status.
func (e *ExitError) ExitCode() int { return e.Code }

// script is s's script with its standard output written to path, whole or
// not at all; the shell exits with the script's status.
func script(s, path string) string {
	return "( " + s + "\n) >" + path + ".part && mv -f " + path + ".part " + path
}

func envFlags(env []string) []string {
	var flags []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		flags = append(flags, "-e", name)
	}
	return flags
}

// Run runs s.Script in a throwaway container of s.Image and returns what
// it printed on stdout, read from a file in the container after it has
// exited: an ExitError when it exited non-zero, ErrNoResult (with the
// container's state) when it exited 0 and left no file, the bytes
// otherwise. The bytes may be empty; whether that is an answer is the
// caller's to say.
//
// The container is named before it is made and removed by that name when
// the read ends, however it ends (the caller stopping included). A removal
// that fails, or a container whose creation was never answered, is left
// to a later read: each read first removes result containers past their
// deadline label, and nothing else.
//
// A caller that has stopped before the container is made is answered with
// its stop and no removal: the removal outlives the stop, so a read begun
// after it would otherwise spend one on a container never made (a cache
// sweep of hundreds of volumes held the daemon's shutdown that way).
func Run(ctx context.Context, dk Docker, s Spec) ([]byte, error) {
	return run(ctx, dk, s, false)
}

// RunOwned reads one result without sweeping any other container. Its container
// has a distinct class and cleanup failure is part of the returned error, naming
// the exact resource that may remain. Callers must retain that error as evidence.
func RunOwned(ctx context.Context, dk Docker, s Spec) ([]byte, error) {
	return run(ctx, dk, s, true)
}

func run(ctx context.Context, dk Docker, s Spec, owned bool) (answer []byte, err error) {
	now := clock.Or(s.Now)
	class := Class
	if owned {
		class += "-owned"
	} else {
		sweep(ctx, dk, now())
	}
	name, err := randomName(Prefix)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if owned {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), removalBound)
			defer cancel()
			out, removeErr := dk(cleanup, nil, "rm", "-f", "-v", name)
			if removeErr != nil {
				answer = nil
				err = errors.Join(err, step("removing owned result", name, removeErr, out))
			}
		}()
	} else {
		defer remove(ctx, dk, "rm", "-f", "-v", name)
	}
	started := now()
	defer func() {
		if err != nil && ctx.Err() == nil {
			err = fmt.Errorf("%w; container %s after %s; %s", err, name, now().Sub(started).Round(time.Millisecond), evidence(ctx, dk, name))
		}
	}()
	create := []string{"create", "--name", name,
		"--label", dockerlabel.Class + "=" + class,
		"--label", dockerlabel.Deadline + "=" + strconv.FormatInt(now().Add(lifetime).Unix(), 10)}
	create = append(create, "--entrypoint", "")
	create = append(create, s.Options...)
	create = append(create, envFlags(s.Env)...)
	create = append(create, s.Image, "sh", "-c", script(s.Script, resultPath))
	if out, err := dk(ctx, s.Env, create...); err != nil {
		return nil, step("creating", name, err, out)
	}
	if out, err := dk(ctx, nil, "start", name); err != nil {
		return nil, step("starting", name, err, out)
	}
	out, err := dk(ctx, nil, "wait", name)
	if err != nil {
		return nil, step("waiting for", name, err, out)
	}
	code, err := strconv.Atoi(lastLine(string(out)))
	if err != nil {
		return nil, fmt.Errorf("waiting for %s: its exit status is not known: %q", name, clip(string(out)))
	}
	if code != 0 {
		said, _ := dk(ctx, nil, "logs", name)
		return nil, &ExitError{Code: code, Said: clip(string(said))}
	}
	answer, err = copyOut(ctx, dk, name, resultPath, limitOf(s))
	return validateAnswer(answer, err, s)
}

// RunIn runs s.Script in the running container and returns what it printed
// on stdout, read from a file it wrote in the container's /tmp: an
// ExitError when it exited non-zero, ErrNoResult when it exited 0 and left
// no file (a container whose /tmp is not writable, or not in its own
// layer, leaves none), the bytes otherwise. s.Image and s.Options are not
// used.
//
// The file is removed when the read ends, under the caller's context: a
// caller that stops is not held up by it, and may leave its few bytes in
// the container's /tmp, which go with the container.
func RunIn(ctx context.Context, dk Docker, container string, s Spec) ([]byte, error) {
	path, err := randomName(resultPath + "-")
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = dk(ctx, nil, "exec", container, "rm", "-f", path, path+".part") }()
	args := append([]string{"exec"}, envFlags(s.Env)...)
	args = append(args, container, "sh", "-c", script(s.Script, path))
	if out, err := dk(ctx, s.Env, args...); err != nil {
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) && exit.ExitCode() > 0 && exit.ExitCode() < 125 {
			// The script's own status (docker exec answers 125 to 127 for
			// itself). What it printed is stderr, or the CLI's words.
			return nil, &ExitError{Code: exit.ExitCode(), Said: clip(string(out))}
		}
		return nil, step("running in", container, err, out)
	}
	answer, err := copyOut(ctx, dk, container, path, limitOf(s))
	if errors.Is(err, ErrNoResult) {
		return nil, fmt.Errorf("%w in container %s", err, container)
	}
	return validateAnswer(answer, err, s)
}

func validateAnswer(answer []byte, err error, s Spec) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if s.Validate != nil {
		if err := s.Validate(answer); err != nil {
			return nil, fmt.Errorf("validating the answer: %w", err)
		}
	}
	return answer, nil
}

func limitOf(s Spec) int64 {
	if s.Max > 0 {
		return s.Max
	}
	return DefaultMax
}

// copyOut copies path out of container with docker cp into a folder of
// its own and reads it. A file that is not there is ErrNoResult.
func copyOut(ctx context.Context, dk Docker, container, path string, limit int64) ([]byte, error) {
	dir, err := os.MkdirTemp("", "apptown-result-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "answer")
	if out, err := dk(ctx, nil, "cp", container+":"+path, local); err != nil {
		if missing(out) {
			return nil, ErrNoResult
		}
		return nil, step("copying the answer out of", container, err, out)
	}
	f, err := os.Open(local)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoResult
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("the answer copied out of %s is not a file", container)
	}
	answer, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(answer)) > limit {
		return nil, fmt.Errorf("the answer in %s is over %d bytes", container, limit)
	}
	return answer, nil
}

// missing is docker cp's answer for a path that is not in the container.
func missing(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "could not find the file") || strings.Contains(s, "no such file or directory")
}

// sweep removes the result containers past their deadline, or with none
// that can be read: those a read could not remove. One another read is
// using is inside its deadline and is left. A sweep that fails changes
// nothing about this read; the next one sweeps again.
func sweep(ctx context.Context, dk Docker, now time.Time) {
	// disk-probe is the predecessor owned by diskspace before result reads
	// shared this lifecycle. Only its expired labelled containers are ours.
	for _, class := range []string{Class, "disk-probe"} {
		sweepClass(ctx, dk, now, class)
	}
}

func sweepClass(ctx context.Context, dk Docker, now time.Time, class string) {
	out, err := dk(ctx, nil, "ps", "-a", "--filter", "label="+dockerlabel.Class+"="+class,
		"--format", `{{.ID}} {{.Label "`+dockerlabel.Deadline+`"}}`)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id, deadline, _ := strings.Cut(strings.TrimSpace(line), " ")
		if id == "" {
			continue
		}
		if at, err := strconv.ParseInt(deadline, 10, 64); err != nil || at < now.Unix() {
			_, _ = dk(ctx, nil, "rm", "-f", "-v", id)
		}
	}
}

// remove runs a removal under its own bound, though the caller has
// stopped: a throwaway container must not outlive its read. A failure is
// left to a later sweep.
func remove(ctx context.Context, dk Docker, args ...string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removalBound)
	defer cancel()
	_, _ = dk(ctx, nil, args...)
}

func randomName(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("naming the result: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func step(what, name string, err error, out []byte) error {
	return fmt.Errorf("%s %s: %w: %s", what, name, err, clip(string(out)))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func clip(s string) string { return strs.Truncate(strings.TrimSpace(s), 300) }
