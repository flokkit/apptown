package preview

import (
	"context"
	"errors"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy/docker"
)

// Change is a source selection resolved through Workflow's checked read
// door. A preview does not accept a caller-supplied project or revision.
type Change struct {
	ID, ProjectID  ids.ID
	SourceRevision string
	// Pinned is a saved commit the preview boots, and nothing else: not the
	// change's folder (a chat on the worker's saved work).
	Pinned string
}

// Result is a booted preview: where it is reached and what it serves.
type Result struct {
	ProjectID ids.ID
	HostPort  int
	URL       string
	Revision  string
	// Incarnation is what the preview serves: its source and the digest of
	// its prepared public values; Public is those values. Kept with the
	// preview for as long as it lives (a forge restart discards previews).
	Incarnation string
	Public      *docker.PublicPrepared
}

// Host prepares the selected source and boots or reuses the provider.
// Successful boot metadata describes the prepared source, including dirty-tree
// edits above Revision. A transient inventory write is best effort, not settlement.
type Host interface {
	BootPreview(context.Context, Change, bool) (Result, error)
}

// BootChange is the trusted execution primitive, also used by automatic chat
// rehearsal. User-facing requests go through the checked preview request.
// Ownership covers source preparation, restart reaping, boot and row recording.
func (p *Runtime) BootChange(ctx context.Context, ch Change, restart bool, host Host) (out Result, err error) {
	if host == nil || ch.ProjectID.IsZero() {
		return Result{}, errors.New("invalid preview execution")
	}
	boot, err := p.Begin(ctx, ChangeKey(ch.ID))
	if err != nil {
		return Result{}, err
	}
	defer func() { boot.Finish(err) }()
	out, err = host.BootPreview(ctx, ch, restart)
	if err != nil {
		return Result{}, err
	}
	out.ProjectID = ch.ProjectID
	return out, nil
}
