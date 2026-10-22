package repositoryedit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
)

// A task attempt's result is collected once, as a candidate in the project's
// repository (design/operations/workers.md, "How work travels"): the
// worker's bundle is imported through the quarantine
// and checked (its structure, the tip it names, descent from the attempt's
// source), and its tip is kept as CandidateRef. Publication moves the run's
// branch to that ref by compare-and-swap; a refused one is renamed to
// RefusedStepRef, where the work stays retrievable (FindRefusedStepWork).
//
// The attempt's source commit, which the candidate must build on, is kept
// beside it on the attempt's task record (tasks.Admission.Source): the
// candidate's ref name carries the step and attempt that key that record.
// Refs under refs/apptown/ are AppTown's; the refs are kept, as a
// publication's history is.

// CandidateRef is where a step attempt's collected result is kept.
func CandidateRef(stepID ids.ID, attempt int64) string {
	return fmt.Sprintf("refs/apptown/candidates/step-%d-a%d", stepID, attempt)
}

// RefusedStepRef is where a step attempt's result whose publication was
// refused is kept, for its download.
func RefusedStepRef(stepID ids.ID, attempt int64) string {
	return fmt.Sprintf("refs/apptown/refused/step-%d-a%d", stepID, attempt)
}

// Candidate is a step attempt's collected result in the project's
// repository.
type Candidate struct {
	// Ref is the ref it is kept under: CandidateRef, or RefusedStepRef once
	// its publication was refused.
	Ref string
	// Tip is its commit; Source the commit the attempt was given. An attempt
	// that changed nothing has Tip == Source.
	Tip, Source string
	// Refused: its publication was refused, and it is kept under Ref.
	Refused bool
}

// Changed reports whether the attempt made commits on its source.
func (c Candidate) Changed() bool { return c.Tip != c.Source }

// FindCandidate returns the attempt's result if it was collected already: a
// kept candidate, or its refusal. A ref that does not build on source is not
// the attempt's result and is an error, never taken as it.
func FindCandidate(ctx context.Context, hub string, stepID ids.ID, attempt int64, source string) (Candidate, bool, error) {
	for _, c := range []Candidate{{Ref: CandidateRef(stepID, attempt)}, {Ref: RefusedStepRef(stepID, attempt), Refused: true}} {
		tip := gitexec.HubRef(ctx, hub, c.Ref)
		if tip == "" {
			continue
		}
		if !gitexec.HubAncestor(ctx, hub, source, tip) {
			return Candidate{}, false, fmt.Errorf("%s holds %s, which does not build on the attempt's source %s", c.Ref, tip, source)
		}
		c.Tip, c.Source = tip, source
		return c, true, nil
	}
	return Candidate{}, false, nil
}

// KeepCandidate stores a step attempt's collected result as its candidate.
// bundle is the worker's bundle of refs/heads/work, its tip reported as
// reported; "" (and no bundle) when the attempt changed nothing, whose
// candidate is then source itself. The bundle is imported through the
// quarantine and checked; a malformed one, a tip other than the one
// reported, or one not built on source is refused (gitexec.ErrBundleRefused)
// and kept nowhere. The candidate exists once this returns without error.
func KeepCandidate(ctx context.Context, hub string, stepID ids.ID, attempt int64, source, reported, bundle string) (Candidate, error) {
	if !gitexec.IsObjectID(source) {
		return Candidate{}, fmt.Errorf("an attempt's candidate needs its source commit, not %q", source)
	}
	ref := CandidateRef(stepID, attempt)
	tip := source
	if reported != "" {
		dir, imported, cleanup, err := gitexec.Quarantine(ctx, hub, bundle, "work", "work", source)
		if err != nil {
			return Candidate{}, err
		}
		defer cleanup()
		if imported != reported {
			return Candidate{}, fmt.Errorf("%w: the worker reported %s but its bundle holds %s", gitexec.ErrBundleRefused, reported, imported)
		}
		// The checked objects come into the hub under the candidate's ref, and
		// only there: no branch moves.
		if out, err := gitexec.HubRun(ctx, hub, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--end-of-options", dir, "+refs/heads/work:"+ref); err != nil {
			return Candidate{}, fmt.Errorf("keeping the attempt's result: %w: %s", err, out)
		}
		tip = imported
	} else if out, err := gitexec.HubRun(ctx, hub, "update-ref", ref, source, ""); err != nil && gitexec.HubRef(ctx, hub, ref) != source {
		return Candidate{}, fmt.Errorf("keeping the attempt's result: %w: %s", err, out)
	}
	if got := gitexec.HubRef(ctx, hub, ref); got != tip {
		return Candidate{}, fmt.Errorf("keeping the attempt's result: %s holds %q, not %s", ref, got, tip)
	}
	return Candidate{Ref: ref, Tip: tip, Source: source}, nil
}

// refuseCandidate renames a candidate to its refused ref in one ref
// transaction, and returns it as refused. An error means the work may not
// be kept: the caller holds what it came from.
func refuseCandidate(ctx context.Context, hub string, c Candidate) (Candidate, error) {
	if c.Refused {
		return c, nil
	}
	stepAttempt, ok := strings.CutPrefix(c.Ref, "refs/apptown/candidates/")
	if !ok {
		return c, fmt.Errorf("%s is not a candidate", c.Ref)
	}
	refused := "refs/apptown/refused/" + stepAttempt
	script := fmt.Sprintf("update %s %s\ndelete %s %s\n", refused, c.Tip, c.Ref, c.Tip)
	if _, err := gitexec.HubOutput(ctx, hub, strings.NewReader(script), "update-ref", "--stdin"); err != nil {
		if gitexec.HubRef(ctx, hub, refused) != c.Tip {
			return c, fmt.Errorf("%w: keeping the refused result under %s: %w", ErrResultNotKept, refused, err)
		}
	}
	return Candidate{Ref: refused, Tip: c.Tip, Source: c.Source, Refused: true}, nil
}

// ErrResultNotKept: a task's result could not be kept in the project's
// repository. Its worker workspace must be held.
var ErrResultNotKept = errors.New("the task's result is not kept")

// RefusedStepWork is a step attempt's refused result, kept for its
// download.
type RefusedStepWork struct {
	hub, branch, ref string
	// Commit is the refused result's tip.
	Commit string
}

// FindRefusedStepWork finds the refused result kept for attempt of a run's
// step. A step that is not the run's, or an attempt with none kept, is
// NotFound.
func FindRefusedStepWork(ctx context.Context, db database.Queryer, dataDir string, runID ids.ID, stepID ids.ID, attempt int64) (RefusedStepWork, error) {
	step, err := workflow.ReadStepIn(ctx, db, stepID)
	if err != nil || step.RunID != runID {
		return RefusedStepWork{}, errNoRefusedStep
	}
	run, err := workflow.ReadRunIn(ctx, db, runID)
	if err != nil {
		return RefusedStepWork{}, err
	}
	source, err := workflow.ReadSourceIn(ctx, db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return RefusedStepWork{}, err
	}
	kept := RefusedStepWork{hub: source.Repository(dataDir), branch: source.Branch, ref: RefusedStepRef(stepID, attempt)}
	if source.Branch == "" || !source.RepoBacked() {
		return RefusedStepWork{}, errNoRefusedStep
	}
	if kept.Commit = gitexec.HubRef(ctx, kept.hub, kept.ref); kept.Commit == "" {
		return RefusedStepWork{}, errNoRefusedStep
	}
	return kept, nil
}

// Bundle writes a bundle of the refused result, less what the run's branch
// holds, to w. Anyone who fetches it gets the commits AppTown refused and
// can repair them; nothing about it is published.
func (k RefusedStepWork) Bundle(ctx context.Context, w io.Writer) error {
	return bundleKept(ctx, k.hub, k.branch, k.ref, errNoRefusedStep, w)
}

var errNoRefusedStep = fault.New(fault.NotFound, "no refused result is kept for this step attempt")

// bundleKept writes a bundle of a kept refusal (a ref under
// refs/apptown/refused/), less what branch already holds, to w. missing is
// returned when the ref names nothing.
func bundleKept(ctx context.Context, hub, branch, ref string, missing error, w io.Writer) error {
	if !strings.HasPrefix(ref, "refs/apptown/refused/") || gitexec.HubRef(ctx, hub, ref) == "" {
		return missing
	}
	args := []string{"bundle", "create", "-q", "-", ref}
	if gitexec.HubRef(ctx, hub, "refs/heads/"+branch) != "" {
		args = append(args, "^refs/heads/"+branch)
	}
	if err := gitexec.HubStream(ctx, hub, w, args...); err != nil {
		return fmt.Errorf("bundling the refused work: %w", err)
	}
	return nil
}

// hubOf is a project's repository under the data directory.
func hubOf(dataDir, projectSlug string) string {
	return filepath.Join(dataDir, "workspaces", "project-"+projectSlug)
}
