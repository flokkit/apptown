package workflow

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// A change has at most one holder, the one worker allowed to change it: a
// chat, a writing run (any root run but a landing) or a desktop ticket
// (stock/docs/guides/changes-and-main.md, "One worker at a time"). The holder
// is the change's branch authority. A holder is never taken from its owner:
// the next worker takes the change only once the holder has ended, which is
// judged when it asks, from the owners' own records, so a crash cannot leave
// a change held by something that is gone.

// Presence is what the holder rule asks the owners of chats, of task
// records and of containers: whether a chat has ended with nothing of it
// left (console.Service.Gone), which runs have a task not yet settled
// (tasks), and whether anything is still writing the change's folder. A
// nil Presence knows no chat to have ended and no task, and cannot see
// containers (unit tests without them).
type Presence interface {
	Gone(ctx context.Context, chatID string) (bool, error)
	// ChatEnded is Gone without removing anything.
	ChatEnded(ctx context.Context, chatID string) (bool, error)
	UnsettledRuns(ctx context.Context, q database.Queryer) ([]ids.ID, error)
	// FolderClear removes what is left writing the change's folder (any
	// container labelled as mounting it writable) and reports whether
	// Docker confirms none remains.
	FolderClear(ctx context.Context, changeID ids.ID) (bool, error)
}

// ChangeHeld refuses a worker because another holds the change.
type ChangeHeld struct{ Holder string }

func (e *ChangeHeld) Error() string {
	kind, id, _ := strings.Cut(e.Holder, ":")
	switch kind {
	case "chat":
		return "a chat is working on this change; open it or end it"
	case "run":
		return "a workflow (run " + id + ") is working on this change; wait for it or stop it"
	case "ticket":
		return "a desktop checkout holds this change until its push is synced or it expires"
	case "folder":
		return "something an earlier worker started is still running in this change's folder and could not be stopped; try again once it has gone"
	}
	return "this change is held by " + e.Holder
}
func (*ChangeHeld) FaultKind() fault.Kind { return fault.Conflict }
func (*ChangeHeld) FaultCode() string     { return "change-held" }

// TakeChangeIn makes holder the holder of the change's branch, under the
// branch's lock, when the current holder has ended; otherwise it refuses
// with *ChangeHeld. A holder taking again keeps its generation.
func TakeChangeIn(ctx context.Context, tx *database.Tx, projectID ids.ID, branch, holder string, chats Presence, at time.Time) (scm.Authority, error) {
	current, err := scm.LockAuthorityIn(ctx, tx, projectID, branch)
	if err != nil || current.Holder == holder {
		return current, err
	}
	ended, err := HolderEndedIn(ctx, tx, projectID, branch, current.Holder, chats, at)
	if err != nil {
		return scm.Authority{}, err
	}
	if !ended {
		held := current.Holder
		if recorded, err := holderRecordsEndedIn(ctx, tx, projectID, current.Holder, chats, false, at); err == nil && recorded {
			held = "folder" // its owner has ended; a container of it has not
		}
		return scm.Authority{}, &ChangeHeld{Holder: held}
	}
	return scm.AdvanceAuthorityIn(ctx, tx, projectID, branch, holder)
}

// HolderEndedIn reports whether nothing of holder can still change the
// change: a run whose tree has finished or been abandoned, with no step
// still running and no task unsettled; a ticket revoked, expired or gone (a
// used ticket holds until its sync releases it); a chat ended, with its
// runtime confirmed removed; a momentary request no longer running. Then,
// whoever held it, nothing may still be writing the change's folder: every
// container labelled as mounting it writable is removed, and Docker must
// confirm none remains. That also finds what records cannot: a runtime
// whose record was lost, an agent's private Docker daemon, an agent left by
// an earlier process. Anything unknown is not assumed to have ended.
//
// It removes containers, so it is called only with the branch's authority
// locked (TakeChangeIn, landBesideHolderIn): a caller holding a stale view
// of the holder could otherwise remove a worker admitted since.
func HolderEndedIn(ctx context.Context, q database.Queryer, projectID ids.ID, branch, holder string, chats Presence, at time.Time) (bool, error) {
	ended, err := holderRecordsEndedIn(ctx, q, projectID, holder, chats, true, at)
	if err != nil || !ended || chats == nil {
		return ended, err
	}
	changeID, err := workflowsql.New(q.SQL()).ChangeIDForBranch(ctx, workflowsql.ChangeIDForBranchParams{ProjectID: projectID, Branch: branch})
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, database.QueryError(err, "reading the change of a branch")
	}
	return chats.FolderClear(ctx, changeID)
}

// HolderRecordsEndedIn is what the holder's own records say. It changes
// nothing, so it serves a check made before the branch's authority is
// locked; only HolderEndedIn under that lock removes what is left in the
// folder, since by then no new worker can have taken the change.
func HolderRecordsEndedIn(ctx context.Context, q database.Queryer, projectID ids.ID, holder string, chats Presence, at time.Time) (bool, error) {
	return holderRecordsEndedIn(ctx, q, projectID, holder, chats, false, at)
}

// holderRecordsEndedIn is HolderRecordsEndedIn, except that with remove a
// chat's leftover runtime is removed before it is judged (Gone), as a take
// under the lock may.
func holderRecordsEndedIn(ctx context.Context, q database.Queryer, projectID ids.ID, holder string, chats Presence, remove bool, at time.Time) (bool, error) {
	if (scm.Authority{Holder: holder}).Free() {
		return true, nil // released, or never handed to anyone
	}
	kind, id, _ := strings.Cut(holder, ":")
	switch kind {
	case "run":
		root, err := ids.Parse(id)
		if err != nil {
			return false, fmt.Errorf("reading holder %q: %w", holder, err)
		}
		tree, err := workflowsql.New(q.SQL()).RunTreeActivity(ctx, workflowsql.RunTreeActivityParams{RootID: root, ProjectID: projectID,
			RunRunning: RunRunning.String(), StepRunning: StatusRunning.String()})
		if err != nil {
			return false, database.QueryError(err, "reading a holding run")
		}
		if tree.Running || tree.Stepping {
			return false, nil
		}
		if chats == nil {
			return true, nil
		}
		unsettled, err := chats.UnsettledRuns(ctx, q)
		if err != nil {
			return false, err
		}
		for _, run := range tree.RunIds {
			if slices.Contains(unsettled, run) {
				return false, nil
			}
		}
		return true, nil
	case "ticket":
		ticket, err := ids.Parse(id)
		if err != nil {
			return false, fmt.Errorf("reading holder %q: %w", holder, err)
		}
		holds, err := identity.TicketHoldsIn(ctx, q, ticket, at)
		return !holds, err
	case "chat":
		if chats == nil {
			return false, nil
		}
		if !remove {
			return chats.ChatEnded(ctx, id)
		}
		return chats.Gone(ctx, id)
	case "request":
		// A request holds only while this process runs it; one left by a
		// crash has ended with its process.
		requests.Lock()
		defer requests.Unlock()
		return !requests.live[holder], nil
	}
	return false, nil
}

// ReleaseChangeIn ends holder's hold on the change's branch, if it still
// holds it: a ticket's sync.
func ReleaseChangeIn(ctx context.Context, tx *database.Tx, projectID ids.ID, branch, holder string) error {
	_, err := scm.ReleaseAuthorityIn(ctx, tx, projectID, branch, holder)
	return err
}

// requests are the momentary holders this process is running: a landing's
// admission or a desktop checkout publishing the folder's committed work,
// or the services editor writing the application declaration, while nobody
// else holds the change. Each has its own name, so a delayed request can
// never publish under another's grant.
var requests = struct {
	sync.Mutex
	live map[string]bool
	next int64
}{live: map[string]bool{}}

// TakeRequestIn takes the change for a momentary request of kind (for
// example "landing") and returns its holder name; release ends it and
// gives the change back. It refuses with *ChangeHeld like any take.
func TakeRequestIn(ctx context.Context, db *database.DB, projectID ids.ID, branch, kind string, chats Presence, at time.Time) (string, func(context.Context) error, error) {
	requests.Lock()
	requests.next++
	// A random part keeps a name unique across restarts; the count, within one.
	holder := fmt.Sprintf("request:%s-%s-%d", kind, rand.Text(), requests.next)
	requests.live[holder] = true
	requests.Unlock()
	end := func() {
		requests.Lock()
		delete(requests.live, holder)
		requests.Unlock()
	}
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		_, err := TakeChangeIn(ctx, tx, projectID, branch, holder, chats, at)
		return err
	}); err != nil {
		end()
		return "", nil, err
	}
	release := func(ctx context.Context) error {
		defer end()
		return db.InTx(ctx, func(tx *database.Tx) error {
			return ReleaseChangeIn(ctx, tx, projectID, branch, holder)
		})
	}
	return holder, release, nil
}
