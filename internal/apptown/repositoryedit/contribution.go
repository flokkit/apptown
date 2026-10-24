package repositoryedit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// Contributions orders local-checkout credentials and workspace adoption. It
// shares the existing authority, Work fencing and SCM operations across transports.
type Contributions struct {
	db       *database.DB
	work     *workflow.Service
	identity *identity.Service
	projects *project.Projects
	branches *scm.Branches
	dataDir  string
	chats    workflow.Presence
	locks    *scm.RepositoryLocks
}

func (c *Contributions) now() time.Time { return workClock(c.work)() }

func NewContributions(db *database.DB, work *workflow.Service, identity *identity.Service, projects *project.Projects, branches *scm.Branches, dataDir string, chats workflow.Presence, locks *scm.RepositoryLocks) *Contributions {
	return &Contributions{db, work, identity, projects, branches, dataDir, chats, locks}
}

type ContributionRefusal string

func (r ContributionRefusal) Error() string { return string(r) }
func contributionRefusal(format string, args ...any) error {
	return ContributionRefusal(fmt.Sprintf(format, args...))
}

type Checkout struct {
	// Ref is the Change's branch, to fetch. PushRef is where the ticket's
	// push lands before AppTown publishes it to Ref.
	Token, Tip, Ref, PushRef string
	Ticket                   *identity.Ticket
}
type SyncedRevision struct{ Tip, PreviousTip string }

func (c *Contributions) authorize(ctx context.Context, action string, ch *workflow.Change) error {
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified {
		return access.ErrPrincipalRequired
	}
	pr := caller.Principal
	dec, err := c.identity.Decide(ctx, pr, access.Permission{Capability: access.CapChangeWriteRevision, Resource: workflow.ChangeResource(ctx, c.projects, ch), Context: access.Context{Consequential: true, Route: "mcp:" + action}})
	if err != nil {
		return err
	}
	if !dec.Allow {
		return contributionRefusal("%s", dec.Reason)
	}
	return nil
}
func (c *Contributions) Checkout(ctx context.Context, changeID ids.ID) (Checkout, error) {
	ch, p, err := c.change(ctx, changeID)
	if err != nil {
		return Checkout{}, err
	}
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified {
		return Checkout{}, access.ErrPrincipalRequired
	}
	if p.DefaultBranch == "" || ch.Branch == "" {
		return Checkout{}, contributionRefusal("change %s#%d has no repository branch to push to (the project is not repo-backed)", p.Slug, ch.Number)
	}
	if ch.State != workflow.ChangeInProgress {
		return Checkout{}, contributionRefusal("change %s#%d is %s; only an in-progress Change takes revisions", p.Slug, ch.Number, ch.State)
	}
	if err := c.authorize(ctx, "checkout_change", ch); err != nil {
		return Checkout{}, err
	}
	hub := filepath.Join(c.dataDir, "workspaces", "project-"+p.Slug)
	// Who holds the change is read before anything is minted: another
	// holder refuses the checkout, and the caller's own earlier ticket is
	// replaced (a renewal), not handed over.
	current, err := scm.ReadAuthorityIn(ctx, c.db, p.ID, ch.Branch)
	if err != nil {
		return Checkout{}, err
	}
	if ticket, isTicket := strings.CutPrefix(current.Holder, "ticket:"); isTicket {
		id, _ := ids.Parse(ticket)
		// The caller's own earlier ticket is the same person's: compared by
		// principal, so a renamed caller still renews theirs.
		if person, err := identity.TicketPersonIn(ctx, c.db, id); err != nil {
			return Checkout{}, err
		} else if mine, err := identity.ActorIn(ctx, c.db, caller.Principal.Name); err != nil {
			return Checkout{}, err
		} else if !person.IsZero() && (person == caller.Principal.ID || person == mine.Principal) {
			if err := c.identity.RevokeChangeTickets(ctx, ch.ID); err != nil {
				return Checkout{}, err
			}
		}
	}
	if ended, err := workflow.HolderRecordsEndedIn(ctx, c.db, p.ID, current.Holder, c.chats, c.now()); err != nil {
		return Checkout{}, err
	} else if !ended {
		return Checkout{}, contributionRefusal("%s", (&workflow.ChangeHeld{Holder: current.Holder}).Error())
	}
	if err := c.publishFolder(ctx, p, ch, hub); err != nil {
		return Checkout{}, err
	}
	ref := "refs/heads/" + ch.Branch
	tip := scm.Ref(ctx, hub, ref)
	tok, t, err := c.identity.IssueTicket(ctx, caller.Principal.Name, caller.Principal.DelegationID, ch.ID, ref, tip)
	if err != nil {
		return Checkout{}, err
	}
	// The ticket takes the change, and holds it until its push is synced or
	// it expires. Another holder that has not ended (a chat, a run, another
	// checkout) refuses it; the unusable ticket is revoked. The desktop works
	// from the branch as published: nothing is published for it here.
	// The receipt of the credential is written with the hold it takes: a
	// ticket whose receipt failed is revoked like one that could not hold.
	if _, err := database.Transact(ctx, c.db, func(tx *database.Tx) (scm.Authority, error) {
		held, err := workflow.TakeChangeIn(ctx, tx, p.ID, ch.Branch, scm.TicketHolder(t.ID), c.chats, c.now())
		if err != nil {
			return held, err
		}
		return held, audit.At(c.now()).RecordIn(ctx, tx, caller.Receipt, p.ID, "change.ticket-minted", fmt.Sprintf("change %d", ch.ID),
			map[string]any{"ref": ref, "expectedTip": tip, "ticketId": t.ID, "expiresAt": t.ExpiresAt})
	}); err != nil {
		// The ticket was issued: revoke it though the caller has gone.
		if revokeErr := c.identity.RevokeChangeTickets(context.WithoutCancel(ctx), ch.ID); revokeErr != nil {
			return Checkout{}, fmt.Errorf("%v; and revoking the unusable ticket: %w", err, revokeErr)
		}
		var held *workflow.ChangeHeld
		if errors.As(err, &held) {
			return Checkout{}, contributionRefusal("%s", held.Error())
		}
		return Checkout{}, err
	}
	return Checkout{Token: tok, Ticket: t, Tip: tip, Ref: ref, PushRef: scm.IncomingRef(t.ID)}, nil
}

// publishFolder publishes what the change's folder committed (its previous
// worker's work) before a desktop checkout, so the desktop starts from it:
// under a grant the request takes and at once releases. Another holder that
// has not ended refuses the checkout.
func (c *Contributions) publishFolder(ctx context.Context, p *project.Project, ch *workflow.Change, hub string) error {
	ws := filepath.Join(c.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if !gitexec.IsRepo(ws) {
		return nil
	}
	// A folder switched off the change's branch would strand its work.
	if on, _ := gitexec.Output(ctx, ws, "symbolic-ref", "--quiet", "--short", "HEAD"); on != ch.Branch {
		return contributionRefusal("the change's folder is on %s, not the change's branch %s; switch it back first", strs.FirstNonBlank(on, "no branch"), ch.Branch)
	}
	tip := scm.Ref(ctx, ws, "refs/heads/"+ch.Branch)
	if tip == "" || scm.Contains(ctx, hub, ch.Branch, tip) {
		return nil
	}
	holder, release, err := workflow.TakeRequestIn(ctx, c.db, p.ID, ch.Branch, "checkout", c.chats, c.now())
	if err != nil {
		var held *workflow.ChangeHeld
		if errors.As(err, &held) {
			return contributionRefusal("%s", held.Error())
		}
		return err
	}
	_, published := c.branches.Publish(ctx, scm.Publication{ProjectID: p.ID, Repository: hub, Branch: ch.Branch, From: ws, Grant: scm.Grant{Holder: holder}})
	if err := release(ctx); err != nil {
		return errors.Join(published, err)
	}
	return published
}

func (c *Contributions) Sync(ctx context.Context, changeID ids.ID, pushed, expected string) (SyncedRevision, error) {
	ch, p, err := c.change(ctx, changeID)
	if err != nil {
		return SyncedRevision{}, err
	}
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified {
		return SyncedRevision{}, access.ErrPrincipalRequired
	}
	if ch.State != workflow.ChangeInProgress || ch.Branch == "" {
		return SyncedRevision{}, contributionRefusal("change %s#%d is %s and takes no revisions", p.Slug, ch.Number, ch.State)
	}
	if err := c.authorize(ctx, "sync_change", ch); err != nil {
		return SyncedRevision{}, err
	}
	pushed = strings.TrimSpace(pushed)
	expected = strings.TrimSpace(expected)
	hub := filepath.Join(c.dataDir, "workspaces", "project-"+p.Slug)
	ref := "refs/heads/" + ch.Branch
	hubTip := scm.Ref(ctx, hub, ref)
	if hubTip == "" || !strings.HasPrefix(hubTip, pushed) {
		return SyncedRevision{}, contributionRefusal("the hub's %s is %s, not %s: the push did not arrive, or another writer moved it; fetch and look", ch.Branch, strs.ShortSHA(hubTip), strs.ShortSHA(pushed))
	}
	// Preparing a missing folder takes the repository lock itself.
	ws := filepath.Join(c.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if !gitexec.IsRepo(ws) {
		if err := c.work.PrepareWorkspace(ctx, ch.ID); err != nil {
			return SyncedRevision{}, contributionRefusal("the workspace could not be prepared: %v", err)
		}
	}
	// The sync holds the repository lock from its holder check until its
	// release. The next worker prepares the folder under the same lock, so
	// it cannot write the folder while the sync resets it, even if the
	// ticket expires or is revoked meanwhile.
	release, err := c.locks.Acquire(ctx, p.ID)
	if err != nil {
		return SyncedRevision{}, err
	}
	defer release()
	// Only the caller's ticket that holds the change, still valid, syncs it.
	authority, err := scm.ReadAuthorityIn(ctx, c.db, p.ID, ch.Branch)
	if err != nil {
		return SyncedRevision{}, err
	}
	holder := authority.Holder
	ticketID, isTicket := strings.CutPrefix(holder, "ticket:")
	id, _ := ids.Parse(ticketID)
	person, err := identity.TicketPersonIn(ctx, c.db, id)
	if err != nil {
		return SyncedRevision{}, err
	}
	mine, err := identity.ActorIn(ctx, c.db, caller.Principal.Name)
	if err != nil {
		return SyncedRevision{}, err
	}
	holds, err := identity.TicketHoldsIn(ctx, c.db, id, c.now())
	if err != nil {
		return SyncedRevision{}, err
	}
	yours := !person.IsZero() && (person == caller.Principal.ID || person == mine.Principal)
	if !isTicket || !yours || !holds {
		return SyncedRevision{}, contributionRefusal("change %s#%d is not held by your desktop checkout (its ticket may have expired); the hub has the revision, and whoever works on the change next starts from it", p.Slug, ch.Number)
	}
	wsTip, _ := gitexec.Run(ctx, ws, "rev-parse", "--verify", "--quiet", "HEAD")
	wsTip = strings.TrimSpace(wsTip)
	if err := scm.StageAll(ctx, ws); err != nil {
		return SyncedRevision{}, err
	}
	// What StageAll staged is the work an adoption would discard; the
	// AppTown's untracked runtime files under .apptown survive the reset.
	if staged, err := gitexec.Staged(ctx, ws); err != nil {
		return SyncedRevision{}, err
	} else if staged {
		return SyncedRevision{}, contributionRefusal("the Change's workspace has uncommitted work (a chat or hand edits); the hub has your revision, but adopting it would discard that work. Land or sweep it in AppTown first")
	}
	if expected != "" && wsTip != "" && !strings.HasPrefix(wsTip, expected) && wsTip != hubTip {
		return SyncedRevision{}, contributionRefusal("the workspace was at %s, not the %s you expected: another writer advanced it. Fetch %s, merge, push again with a fresh ticket",
			strs.ShortSHA(wsTip), strs.ShortSHA(expected), ch.Branch)
	}
	// Adopt: bring the hub's objects over (into FETCH_HEAD; git refuses
	// to fetch straight into the checked-out branch) and move the
	// branch to the pushed tip.
	if out, err := gitexec.Run(ctx, ws, "fetch", "--quiet", hub, ref); err != nil {
		return SyncedRevision{}, contributionRefusal("adopting the revision failed: %s", strings.TrimSpace(out))
	}
	if out, err := gitexec.Run(ctx, ws, "checkout", "--quiet", "--force", ch.Branch); err != nil {
		return SyncedRevision{}, contributionRefusal("adopting the revision failed: %s", strings.TrimSpace(out))
	}
	if out, err := gitexec.Run(ctx, ws, "reset", "--quiet", "--hard", hubTip); err != nil {
		return SyncedRevision{}, contributionRefusal("adopting the revision failed: %s", strings.TrimSpace(out))
	}
	// The sync ends the ticket's hold: the next worker can take the change.
	// The folder has moved, so the release is written though the caller
	// has gone.
	ctx = context.WithoutCancel(ctx)
	if err := c.db.InTx(ctx, func(tx *database.Tx) error {
		return workflow.ReleaseChangeIn(ctx, tx, p.ID, ch.Branch, holder)
	}); err != nil {
		return SyncedRevision{}, err
	}
	audit.At(c.now()).BestEffort(ctx, c.db, caller.Receipt, p.ID, "change.revision-synced", fmt.Sprintf("change %d", ch.ID),
		map[string]any{"from": wsTip, "to": hubTip, "ref": ref})
	return SyncedRevision{Tip: hubTip, PreviousTip: wsTip}, nil
}

// PublishPush moves a Change's branch to what a ticket pushed into its
// holding ref, if the ticket still holds the branch and the branch is still
// where the ticket expected. A refusal is audited and returned. The holding
// ref is removed either way; sync_change then reports where the branch is.
//
// The ticket is the authority here: the Git transport has already resolved it
// from the pushed credential. There is no caller identity to check.
func (c *Contributions) PublishPush(ctx context.Context, ticket *identity.Ticket) error {
	ch, err := workflow.ReadChangeIn(ctx, c.db, ticket.ChangeID)
	if err != nil {
		return err
	}
	p, err := c.projects.ByID(ctx, ch.ProjectID)
	if err != nil {
		return err
	}
	hub := filepath.Join(c.dataDir, "workspaces", "project-"+p.Slug)
	incoming := scm.IncomingRef(ticket.ID)
	pushed := scm.Ref(ctx, hub, incoming)
	if pushed == "" {
		return nil
	}
	defer func() {
		if out, err := gitexec.Run(context.WithoutCancel(ctx), hub, "update-ref", "-d", incoming); err != nil {
			log.Printf("git ticket %d: removing the holding ref: %v: %s", ticket.ID, err, out)
		}
	}()
	_, err = c.branches.Publish(ctx, scm.Publication{ProjectID: p.ID, Repository: hub, Branch: ch.Branch,
		Incoming: incoming, Expected: ticket.ExpectedTip, Grant: scm.Grant{Holder: scm.TicketHolder(ticket.ID), Valid: ticketStillHolds(ticket.ID, c.now)}})
	if err != nil {
		audit.At(c.now()).BestEffort(ctx, c.db, ticket.User+" via git ticket", p.ID, "change.revision-refused", fmt.Sprintf("change %d", ch.ID),
			map[string]any{"ref": ticket.Ref, "pushed": pushed, "ticketId": ticket.ID, "reason": err.Error()})
	}
	return err
}

// ticketStillHolds refuses a push whose ticket expired or was revoked after
// Git accepted it: the ticket is read again where the publication is
// admitted, so a revocation before that point wins.
func ticketStillHolds(id ids.ID, now func() time.Time) func(context.Context, *database.Tx) error {
	return func(ctx context.Context, tx *database.Tx) error {
		holds, err := identity.TicketHoldsIn(ctx, tx, id, now())
		if err != nil {
			return err
		}
		if !holds {
			return contributionRefusal("the checkout's ticket expired or was revoked before its push was published; ask for a new checkout and push again")
		}
		return nil
	}
}

// mcpChangeDiff produces the Change's diff the same way the web page does
// (httpapi ChangeDiff): from the change's base (scm.ChangeBase, which
// survives catching up with main) to its branch. With a recorded fork point
// it reads the Change's workspace, so unpublished commits are included
// without a read publishing the branch.
func (c *Contributions) Diff(ctx context.Context, changeID ids.ID) (string, error) {
	ch, p, err := c.change(ctx, changeID)
	if err != nil {
		return "", err
	}
	return changePatch(ctx, c.dataDir, p, ch)
}

// changePatch is Diff's body once the change is read: the committed
// comparison the web page reads (scm.ReadChangeDiff), and its patch. A diff
// Git could not produce is an error, never "no difference".
func changePatch(ctx context.Context, dataDir string, p *project.Project, ch *workflow.Change) (string, error) {
	if ch.Branch == "" {
		return "", contributionRefusal("change %s#%d has no branch yet, so there is nothing to diff", p.Slug, ch.Number)
	}
	repo := filepath.Join(dataDir, "workspaces", "project-"+p.Slug)
	if ws := filepath.Join(dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID)); ch.BaseSHA != "" && gitexec.IsRepo(ws) {
		repo = ws
	}
	if _, err := gitexec.Run(ctx, repo, "rev-parse", "--verify", ch.Branch); err != nil {
		return "", contributionRefusal("branch %s of change %s#%d is gone from the repository", ch.Branch, p.Slug, ch.Number)
	}
	hub := filepath.Join(dataDir, "workspaces", "project-"+p.Slug)
	comparison, err := scm.ReadChangeDiff(ctx, hub, repo, p.DefaultBranch, ch.BaseSHA, ch.Branch)
	if err != nil {
		return "", err
	}
	if comparison.Empty {
		return "", nil
	}
	return comparison.Patch(ctx, repo)
}

// Reload the scoped facts at the command boundary. Transport-supplied record
// fields cannot choose another project's hub, or invent a writable branch.
func (c *Contributions) change(ctx context.Context, id ids.ID) (*workflow.Change, *project.Project, error) {
	ch, err := c.work.Change(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	p, err := c.projects.ByID(ctx, ch.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	return ch, &p, nil
}
