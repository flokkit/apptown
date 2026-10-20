package scm

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/gitexec"
)

// Scratch refs (design/subsystems/guidance-checks.md, "Feedback and
// evidence"): an agent step on a worker commits where AppTown cannot see.
// So that `apptown check` can check that work before the step's work is
// collected, the step pushes its HEAD to the one ref its runtime token may
// write, ScratchRef. A scratch commit is checked as feedback only: it is
// never published, never moves a branch, and never stands as a gate's
// evidence; the landing checks the published commit again. The ref is
// removed once its step attempt is no longer live (SweepScratch).

// ScratchPrefix is where every scratch ref of a project's repository lives.
const ScratchPrefix = "refs/apptown/scratch/"

// ScratchRef is the one ref a step attempt's runtime token may push, for
// the change the step's run works on.
func ScratchRef(changeID ids.ID, stepID ids.ID, attempt int64) string {
	return fmt.Sprintf("%schange-%d/step-%d-a%d", ScratchPrefix, changeID, stepID, attempt)
}

// ChangeScratchPrefix holds the scratch refs of one change's step attempts.
func ChangeScratchPrefix(changeID ids.ID) string {
	return fmt.Sprintf("%schange-%d/", ScratchPrefix, changeID)
}

// parseScratchRef reads a ScratchRef back; ok is false for any other name.
func parseScratchRef(ref string) (stepID ids.ID, attempt int64, ok bool) {
	rest, found := strings.CutPrefix(ref, ScratchPrefix)
	if !found {
		return ids.Nil, 0, false
	}
	change, stepAttempt, found := strings.Cut(rest, "/step-")
	if !found || !strings.HasPrefix(change, "change-") {
		return ids.Nil, 0, false
	}
	// The step's id is the 36 characters before "-a": a UUID may itself
	// contain "-a", so the split is by length, not by the first match.
	if len(stepAttempt) < 38 || stepAttempt[36:38] != "-a" {
		return ids.Nil, 0, false
	}
	step, n := stepAttempt[:36], stepAttempt[38:]
	var err error
	if stepID, err = ids.Parse(step); err != nil {
		return ids.Nil, 0, false
	}
	if attempt, err = strconv.ParseInt(n, 10, 64); err != nil {
		return ids.Nil, 0, false
	}
	return stepID, attempt, true
}

// ScratchRefs lists the scratch refs under prefix (ScratchPrefix, or one
// change's) in a project's repository, each with the commit it names.
func ScratchRefs(ctx context.Context, hub, prefix string) (map[string]string, error) {
	if !strings.HasPrefix(prefix, ScratchPrefix) {
		return nil, fmt.Errorf("%s is not under %s", prefix, ScratchPrefix)
	}
	out, err := gitexec.HubOutput(ctx, hub, nil, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
	if err != nil {
		return nil, fmt.Errorf("listing %s in %s: %w", prefix, hub, err)
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ref, sha, ok := strings.Cut(line, " "); ok {
			refs[ref] = sha
		}
	}
	return refs, nil
}

// SweepScratch removes a project's scratch refs whose step attempt live
// reports ended. A ref live cannot answer for is kept, and its error
// returned: an attempt that may still act keeps its scratch. A name that is
// not a ScratchRef is removed: nothing may push one. Each removal is from
// the commit listed, so a ref that moved meanwhile is left for the next
// sweep.
func SweepScratch(ctx context.Context, hub string, live func(stepID ids.ID, attempt int64) (bool, error)) (removed int, err error) {
	refs, err := ScratchRefs(ctx, hub, ScratchPrefix)
	if err != nil {
		return 0, err
	}
	var firstErr error
	for ref, sha := range refs {
		if stepID, attempt, ok := parseScratchRef(ref); ok {
			alive, err := live(stepID, attempt)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("reading whether %s's attempt is live: %w", ref, err)
				}
				continue
			}
			if alive {
				continue
			}
		}
		if out, err := gitexec.HubRun(ctx, hub, "update-ref", "-d", ref, sha); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("removing %s: %w: %s", ref, err, out)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}
