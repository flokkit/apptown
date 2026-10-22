package changeconsole

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/worker"
)

// WorkerChat is one of a change's chats on the worker, with where its work
// stands.
type WorkerChat struct {
	ID string
	// Live: the chat has not ended.
	Live bool
	// LastSave is its last save's record: HasSave is false before its
	// first, and SaveErr says the record could not be read.
	LastSave repositoryedit.ChatSave
	HasSave  bool
	SaveErr  error
	// RecordAsOf is when AppTown last copied its conversation; zero
	// before the first copy.
	RecordAsOf time.Time
	// Refused is the commit of an ended chat's refused last save, kept for
	// its download; "" when there is none.
	Refused string
}

// WorkerChats lists a change's chats on the worker, live and ended, with
// their last saves; an ended chat whose last save record says it was refused
// and kept offers that commit for download. The record decides, not whether
// a kept ref exists.
func (c *Service) WorkerChats(ctx context.Context, ch *workflow.Change) ([]WorkerChat, error) {
	// A chat folder whose record cannot be read is said by the change's
	// transcript, which names it; this list is of the chats that can be.
	records, _, err := c.sessions.Records(conversation.Key(ch.ProjectID, ch.ID))
	if err != nil {
		return nil, err
	}
	p, err := project.ByIDIn(ctx, c.db, ch.ProjectID)
	if err != nil {
		return nil, err
	}
	hub := filepath.Join(c.dataDir, "workspaces", "project-"+p.Slug)
	out := []WorkerChat{}
	for _, rec := range records {
		if !conversation.OnWorkerRecord(rec.Meta) {
			continue
		}
		save, ok, err := repositoryedit.LastChatSave(rec.Dir)
		v := WorkerChat{ID: rec.ID, Live: rec.Live, LastSave: save, HasSave: ok, SaveErr: err, RecordAsOf: worker.RecordAsOf(rec.Dir)}
		if !rec.Live && err == nil && ok && save.Outcome == repositoryedit.ChatSaveRefused && save.Kept != "" {
			v.Refused = scm.Ref(ctx, hub, save.Kept)
		}
		out = append(out, v)
	}
	return out, nil
}

// RefusedBundle writes an ended chat's refused last save as a Git bundle.
func (c *Service) RefusedBundle(ctx context.Context, ch *workflow.Change, chat string, out io.Writer) error {
	p, err := project.ByIDIn(ctx, c.db, ch.ProjectID)
	if err != nil {
		return err
	}
	return repositoryedit.BundleRefusedChatSave(ctx, c.dataDir, p.Slug, ch.Branch, chat, out)
}
