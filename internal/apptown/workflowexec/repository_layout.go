package workflowexec

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"

	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
)

// RelocationRequest is the exact persisted admission, read without sending it
// to a provider. Its Task may contain credentials; it is never journaled/logged.
type RelocationRequest struct {
	Attempt tasks.Attempt
	Task    execution.TaskSpec
}

// RelocationRequests reads unreleased local/remote admissions before layout
// effects. A missing/unreadable request is unknown, not an empty workspace.
func RelocationRequests(ctx context.Context, db database.Queryer, dataDir string) ([]RelocationRequest, error) {
	attempts, err := tasks.RelocationAttemptsIn(ctx, db)
	if err != nil {
		return nil, err
	}
	reader := TaskRunner{Requests: filepath.Join(dataDir, "tasks")}
	out := make([]RelocationRequest, 0, len(attempts))
	for _, attempt := range attempts {
		req, found, err := reader.storedRequest(attempt.Operation)
		if err != nil {
			return nil, fmt.Errorf("reading task %s before relocation: %w", attempt.Operation, err)
		}
		if !found {
			return nil, fmt.Errorf("task %s has no readable admitted request; settle it before relocation", attempt.Operation)
		}
		if req.Task.Key != attempt.Operation || req.Task.Request != attempt.Digest {
			return nil, fmt.Errorf("task %s request does not match its admission", attempt.Operation)
		}
		out = append(out, RelocationRequest{Attempt: attempt, Task: req.Task})
	}
	return out, nil
}

// RefuseMovedPaths preserves the exact admitted request. Even a remote task's
// source repository is a local input for resending its bundle; SharedWorkspace
// is an opaque backend key and is deliberately never interpreted as a path.
func (r RelocationRequest) RefuseMovedPaths(references []repositorylayout.Reference, oldRoot, newRoot string) error {
	paths := []string{}
	if r.Task.Workspace != "" {
		paths = append(paths, r.Task.Workspace)
	}
	if r.Task.Source != nil {
		paths = append(paths, r.Task.Source.Repository)
	}
	for _, mount := range r.Task.Mounts {
		host, _, ok := strings.Cut(mount, ":")
		if !ok {
			return fmt.Errorf("task %s has an unclassifiable mount; settle it before relocating", r.Attempt.Operation)
		}
		if filepath.IsAbs(host) {
			paths = append(paths, host)
		}
	}
	for _, path := range paths {
		after, matched, err := repositorylayout.Translate(references, r.Attempt.Owner.ProjectID, path)
		if err != nil {
			return fmt.Errorf("task %s local input: %w", r.Attempt.Operation, err)
		}
		rootMoved := oldRoot != newRoot && (path == oldRoot || strings.HasPrefix(path, oldRoot+string(filepath.Separator)))
		if (matched && after != path) || rootMoved {
			return fmt.Errorf("task %s retains an admitted local path that would move; settle and release it under the original layout before relocating", r.Attempt.Operation)
		}
	}
	return nil
}

// RefuseWriter observes only the originally selected backend. An unavailable
// backend, ambiguous identity, or private writer without a release receipt does
// not become evidence that it is safe to change workspace metadata.
func (r RelocationRequest) RefuseWriter(ctx context.Context, driver execution.TaskDriver, backend string) error {
	if driver == nil || backend != r.Attempt.Backend {
		return fmt.Errorf("task %s backend cannot be observed for relocation", r.Attempt.Operation)
	}
	if r.Task.Private != nil {
		return fmt.Errorf("task %s may retain private writers; release it before relocation", r.Attempt.Operation)
	}
	obs, err := driver.Observe(ctx, execution.TaskRef{Key: r.Attempt.Operation, ProviderID: r.Attempt.ProviderID})
	if err != nil {
		return fmt.Errorf("observing task %s before relocation: %w", r.Attempt.Operation, err)
	}
	switch obs.State {
	case execution.TaskAbsent:
		return nil
	case execution.TaskCreated, execution.TaskExited:
		if !owns(r.Attempt, obs) {
			return fmt.Errorf("task %s has ambiguous provider identity", r.Attempt.Operation)
		}
		return nil
	default:
		return fmt.Errorf("task %s still has a writer or unknown state; stop it before relocation", r.Attempt.Operation)
	}
}
