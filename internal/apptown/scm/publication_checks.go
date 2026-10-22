package scm

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
)

// Limits on the new files one publication may bring into the project's
// repository, where history is hard to undo.
const (
	maxPublishedFile  = 10 << 20
	maxPublishedTotal = 50 << 20
)

// ErrNotPublished: a publication was refused for what it would bring in;
// nothing moved, and the work stays where it was made.
var ErrNotPublished = fault.New(fault.Conflict, "the work was not published")

// checkNewWork checks what a publication brings into the branch: every
// commit reachable from proposed and neither from the branch as published
// (current, empty on its first publication) nor from the default branch
// (the hub's HEAD, which only landings move), against each of
// its parents (the change's history, not only its last tree). Nothing may
// change .apptown but its native app contract, no new file may exceed
// maxPublishedFile, and the new files together (each counted once) may not
// exceed maxPublishedTotal. Every publication (a run's step, a chat's
// commits, a desktop push, a worker's result) is checked here, whoever made
// the work.
func checkNewWork(ctx context.Context, repo, proposed, current string) error {
	revs := []string{proposed, "--not"}
	for _, known := range []string{gitexec.HubRef(ctx, repo, "HEAD"), current} {
		if known != "" {
			revs = append(revs, known)
		}
	}
	extra, err := nonSourceIn(ctx, repo, revs...)
	if err != nil {
		return err
	}
	if len(extra) > 0 {
		return fmt.Errorf("%w: it changes files that are not source (%s); only .apptown/app.yaml may change under .apptown", ErrNotPublished, strings.Join(extra, ", "))
	}
	listed, err := gitexec.HubOutput(ctx, repo, nil, append([]string{"rev-list", "--objects"}, revs...)...)
	if err != nil {
		return fmt.Errorf("listing the new objects: %w", err)
	}
	var shas bytes.Buffer
	names := map[string]string{}
	for _, line := range strings.Split(string(listed), "\n") {
		sha, name, _ := strings.Cut(line, " ")
		if sha != "" {
			shas.WriteString(sha + "\n")
			names[sha] = name
		}
	}
	if shas.Len() == 0 {
		return nil
	}
	sized, err := gitexec.HubOutput(ctx, repo, &shas, "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return fmt.Errorf("sizing the new objects: %w", err)
	}
	var total int64
	var large []string
	for _, line := range strings.Split(strings.TrimSpace(string(sized)), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "blob" {
			continue
		}
		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			return fmt.Errorf("sizing %s: %w", f[0], err)
		}
		total += size
		if size > maxPublishedFile {
			large = append(large, fmt.Sprintf("%s (%d MiB)", names[f[0]], size>>20))
		}
	}
	if len(large) > 0 {
		return fmt.Errorf("%w: these files are over %d MiB; remove them from the commits or ignore them: %s", ErrNotPublished, maxPublishedFile>>20, strings.Join(large, ", "))
	}
	if total > maxPublishedTotal {
		return fmt.Errorf("%w: it adds %d MiB of files, over the %d MiB one publication may", ErrNotPublished, total>>20, maxPublishedTotal>>20)
	}
	return nil
}
