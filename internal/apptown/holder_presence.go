package apptown

import (
	"context"
	"log"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// holderPresence answers the holder rule from the owners of chats and of
// task records (workflow.Presence): whether a chat has ended with nothing of
// it left, and which runs still have a task unsettled.
type holderPresence struct {
	sessions *console.Service
	docker   *docker.Scope
	dataDir  string
}

func holderPresenceFor(a *App) holderPresence {
	return holderPresence{a.Console, &a.docker, a.cfg.dataDir}
}

func (h holderPresence) Gone(ctx context.Context, chatID string) (bool, error) {
	return h.sessions.Gone(ctx, chatID)
}

func (h holderPresence) ChatEnded(ctx context.Context, chatID string) (bool, error) {
	return h.sessions.Ended(ctx, chatID)
}

func (holderPresence) UnsettledRuns(ctx context.Context, q database.Queryer) ([]ids.ID, error) {
	return tasks.UnsettledRunsIn(ctx, q)
}

// FolderClear removes every container still labelled as writing the
// change's folder (an agent, its private Docker daemon, a chat or the chat's
// daemon, whoever started it) and reports whether Docker confirms none is
// left.
func (h holderPresence) FolderClear(ctx context.Context, changeID ids.ID) (bool, error) {
	if h.docker.Absent() {
		return true, nil // no local Docker: nothing runs here, and nothing was left at its start
	}
	folder := (workflow.Layout{DataDir: h.dataDir}).ChangeWorkspace(changeID)
	left, err := h.docker.RemoveLabelled(ctx, docker.WritesLabel+"="+folder)
	if err != nil {
		return false, err
	}
	if len(left) > 0 {
		log.Printf("change %d: still writing its folder and could not be removed: %s", changeID, strings.Join(left, ", "))
	}
	return len(left) == 0, nil
}
