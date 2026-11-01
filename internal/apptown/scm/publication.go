package scm

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm/internal/scmsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// Branch publication is guarded: one owner operation checks who may move a branch and what it moves from.
//
//   - Authority: the publisher presents the generation it was admitted under,
//     or acts for the current holder. Every handover or replaced attempt
//     advances the generation, so an earlier publisher is refused even when
//     its commit would be a clean fast-forward.
//   - Content: git moves the branch only from the value this operation read,
//     and only forward unless a rewind is explicitly allowed.
//
// A publication is recorded before git runs and deleted once settled. After a
// crash, the branch's lock file says whether a git process may still finish:
// git holds it from prepare until commit or abort, and aborts on its own if
// AppTown died before sending commit.

var (
	// errAuthoritySuperseded: the publisher's generation or holder is no
	// longer current. Nothing was published.
	errAuthoritySuperseded = errors.New("this work no longer holds the branch: it was handed over or its attempt was replaced")
	// errPublicationUnsettled: an earlier publication of the branch has not
	// been settled. The branch cannot be handed over or published until it is.
	errPublicationUnsettled = errors.New("an earlier publication of this branch is not settled yet")
	// ErrPublicationApplied: the branch moved to the proposed commit, but the
	// publication's record could not be settled. The work is published; the
	// record is settled at the branch's next use or at startup (Recover).
	ErrPublicationApplied = errors.New("the branch moved to the published commit, and its publication is not settled yet")
	// errPublicationUnknown: whether the branch moved is not known. The
	// publication stays recorded and holds the branch until it is settled.
	errPublicationUnknown = errors.New("whether the branch moved is not known yet; the branch is held until it is")
)

// Refused reports whether an error from Publish confirms a refusal: nothing
// moved, by the owner's decision (the branch moved or the proposed commit
// does not build on it, a newer holder or generation, the checks on new
// work). Every other error is not a refusal: the publication may have
// applied (ErrPublicationApplied), its outcome may be unknown
// (ErrPublicationUnknown, ErrPublicationUnsettled), or it failed before
// anything was decided, and asking again later may publish it.
func Refused(err error) bool {
	var moved *branchMoved
	return errors.As(err, &moved) || errors.Is(err, errAuthoritySuperseded) || errors.Is(err, ErrNotPublished)
}

// branchMoved means the branch was not where the publisher expected, or the
// proposed commit does not descend from it. Nothing was published.
type branchMoved struct {
	Branch, Expected, Found, Proposed string
	NotForward                        bool
}

func (e *branchMoved) Error() string {
	if e.NotForward {
		return fmt.Sprintf("branch %s is at %s and %s does not build on it; bring those commits in first", e.Branch, strs.ShortSHA(e.Found), strs.ShortSHA(e.Proposed))
	}
	return fmt.Sprintf("branch %s moved: expected %s, found %s", e.Branch, strs.ShortSHA(e.Expected), strs.ShortSHA(e.Found))
}

// Authority says who may move a branch. A branch nobody has been handed yet
// is generation 1, held by "initial"; the first handover makes it 2.
type Authority struct {
	Generation int64
	Holder     string
}

var initialAuthority = Authority{Generation: 1, Holder: "initial"}

// unassigned reports a branch nobody has been handed yet.
func (a Authority) unassigned() bool { return a.Holder == initialAuthority.Holder }

// RunHolder names a root run as the holder of its change's branch.
func RunHolder(rootRunID ids.ID) string { return fmt.Sprintf("run:%d", rootRunID) }

// ChatHolder names a chat (a console session) as the holder of its change.
func ChatHolder(session string) string { return "chat:" + session }

// TicketHolder names a desktop contributor's push ticket as the holder.
func TicketHolder(ticketID ids.ID) string { return fmt.Sprintf("ticket:%d", ticketID) }

// IncomingRef is where a ticket's push lands. The branch moves only when the
// AppTown publishes it, after checking the ticket still holds the branch.
func IncomingRef(ticketID ids.ID) string {
	return fmt.Sprintf("refs/apptown/incoming/ticket-%d", ticketID)
}

// Grant is what a publisher presents: the exact generation it was admitted
// under, or the holder it acts for synchronously. Exactly one is set.
type Grant struct {
	Generation int64
	Holder     string
	// Valid, when set, is asked inside the publication's admission, after
	// the authority is locked, whether the holder may still publish (a
	// desktop ticket that has since expired or been revoked may not).
	Valid func(context.Context, *database.Tx) error
}

func (g Grant) valid() bool { return (g.Generation > 0) != (g.Holder != "") }
func (g Grant) admits(a Authority) bool {
	if g.Holder != "" {
		return g.Holder == a.Holder
	}
	return g.Generation == a.Generation
}

// ReadAuthorityIn returns who may move the branch, for a publisher to capture
// at admission.
func ReadAuthorityIn(ctx context.Context, q database.Queryer, projectID ids.ID, branch string) (Authority, error) {
	row, err := scmsql.New(q.SQL()).Authority(ctx, scmsql.AuthorityParams{ProjectID: projectID, Branch: branch})
	if errors.Is(err, sql.ErrNoRows) {
		return initialAuthority, nil
	}
	if err != nil {
		return Authority{}, err
	}
	return Authority{row.Generation, row.Holder}, nil
}

// lockAuthorityIn makes the branch's authority row exist and locks it. It is
// the one ordering point for publication admission, handover and attempt
// admission: a branch nobody was handed yet gets its row here, so the first
// handover and the first publication queue on the same lock.
func lockAuthorityIn(ctx context.Context, tx *database.Tx, projectID ids.ID, branch string) (Authority, error) {
	if projectID.IsZero() || branch == "" {
		return Authority{}, fmt.Errorf("branch authority needs its project and branch")
	}
	q := scmsql.New(tx.SQL())
	if err := q.EnsureAuthority(ctx, scmsql.EnsureAuthorityParams{ProjectID: projectID, Branch: branch}); err != nil {
		return Authority{}, err
	}
	row, err := q.AuthorityForUpdate(ctx, scmsql.AuthorityForUpdateParams{ProjectID: projectID, Branch: branch})
	if err != nil {
		return Authority{}, err
	}
	return Authority{row.Generation, row.Holder}, nil
}

// LockAuthorityIn returns who holds the branch and keeps it from changing
// until the caller's transaction ends. Callers that decide a handover from
// other facts (such as whether a run is active) check them after this.
func LockAuthorityIn(ctx context.Context, tx *database.Tx, projectID ids.ID, branch string) (Authority, error) {
	return lockAuthorityIn(ctx, tx, projectID, branch)
}

// advanceLockedIn moves a locked branch to the next generation. It is refused
// while a publication of the branch is unsettled, so a publication admitted
// before a handover finishes before it.
func advanceLockedIn(ctx context.Context, tx *database.Tx, projectID ids.ID, branch, holder string) (Authority, error) {
	q := scmsql.New(tx.SQL())
	if _, err := q.PublicationForBranch(ctx, scmsql.PublicationForBranchParams{ProjectID: projectID, Branch: branch}); err == nil {
		return Authority{}, errPublicationUnsettled
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Authority{}, err
	}
	row, err := q.AdvanceAuthority(ctx, scmsql.AdvanceAuthorityParams{ProjectID: projectID, Branch: branch, Holder: holder})
	if err != nil {
		return Authority{}, err
	}
	return Authority{row.Generation, row.Holder}, nil
}

// AdvanceAuthorityIn hands the branch to holder and revokes every earlier
// generation, including the initial one.
func AdvanceAuthorityIn(ctx context.Context, tx *database.Tx, projectID ids.ID, branch, holder string) (Authority, error) {
	if holder == "" {
		return Authority{}, fmt.Errorf("a branch handover needs its holder")
	}
	if _, err := lockAuthorityIn(ctx, tx, projectID, branch); err != nil {
		return Authority{}, err
	}
	return advanceLockedIn(ctx, tx, projectID, branch, holder)
}

// releasedHolder is the holder of a branch its last holder let go of.
const releasedHolder = "released"

// Free reports a branch nobody holds: never handed to anyone, or released.
func (a Authority) Free() bool { return a.unassigned() || a.Holder == releasedHolder }

// ReleaseAuthorityIn ends holder's hold on the branch, if it still holds it:
// the generation advances to ReleasedHolder, so nothing holder was admitted
// to publish can publish afterwards. A branch another holds is left as it
// is.
func ReleaseAuthorityIn(ctx context.Context, tx *database.Tx, projectID ids.ID, branch, holder string) (bool, error) {
	current, err := lockAuthorityIn(ctx, tx, projectID, branch)
	if err != nil || current.Holder != holder {
		return false, err
	}
	_, err = advanceLockedIn(ctx, tx, projectID, branch, releasedHolder)
	return err == nil, err
}

// ClaimAuthorityIn admits an attempt of holder: it returns the generation the
// attempt publishes under, only while holder still holds the branch. renew
// advances the generation first, revoking the attempt being replaced; it
// never takes the branch from another holder.
func ClaimAuthorityIn(ctx context.Context, tx *database.Tx, projectID ids.ID, branch, holder string, renew bool) (Authority, error) {
	current, err := lockAuthorityIn(ctx, tx, projectID, branch)
	if err != nil {
		return Authority{}, err
	}
	if current.Holder != holder {
		return Authority{}, &heldElsewhere{Branch: branch, Holder: current.Holder}
	}
	if !renew {
		return current, nil
	}
	return advanceLockedIn(ctx, tx, projectID, branch, holder)
}

// heldElsewhere: the branch was handed to another holder. The attempt is not
// admitted to publish; it must not take the branch back.
type heldElsewhere struct{ Branch, Holder string }

func (e *heldElsewhere) Error() string {
	return fmt.Sprintf("branch %s is held by %s now; this work no longer holds it", e.Branch, e.Holder)
}
func (e *heldElsewhere) Is(target error) bool { return target == errAuthoritySuperseded }

// Publication asks to move Branch in Repository (the project's hub) to the
// tip of the same branch in From, a writer's clone, to the commit at
// Incoming, a ref already in Repository, or to Commit, a commit already in
// Repository. Exactly one is given. Incoming and Commit fetch from nothing:
// the move is a compare-and-swap of the ref in the hub.
type Publication struct {
	ProjectID  ids.ID
	Repository string
	Branch     string
	From       string
	Incoming   string
	Commit     string
	// Expected, when set, is the tip the publisher started from. Empty means
	// the branch's current tip; the move must still be forward.
	Expected string
	// Rewind allows moving the branch back to an ancestor (a workflow's
	// restart). It never allows moving sideways.
	Rewind bool
	Grant  Grant
}

type publishedResult struct {
	Previous, Tip string
	Changed       bool
}

// Branches is the only writer of change branches in a project's hub.
type Branches struct {
	db    *database.DB
	locks *RepositoryLocks
	// Now is AppTown's clock, which stamps a publication; nil is the
	// real one.
	Now func() time.Time
}

func (p *Branches) now() time.Time { return clock.Or(p.Now)() }

func NewBranches(db *database.DB, locks *RepositoryLocks) *Branches {
	return &Branches{db: db, locks: locks}
}

func (p *Branches) Publish(ctx context.Context, pub Publication) (publishedResult, error) {
	sources := 0
	for _, s := range []string{pub.From, pub.Incoming, pub.Commit} {
		if s != "" {
			sources++
		}
	}
	if pub.ProjectID.IsZero() || pub.Repository == "" || pub.Branch == "" || sources != 1 || !pub.Grant.valid() {
		return publishedResult{}, fmt.Errorf("a branch publication needs its project, repository, branch, one source and one grant")
	}
	release, err := p.locks.Acquire(ctx, pub.ProjectID)
	if err != nil {
		return publishedResult{}, err
	}
	defer release()
	return p.publishLocked(ctx, pub)
}

// publishLocked runs with the project's repository lock held.
func (p *Branches) publishLocked(ctx context.Context, pub Publication) (publishedResult, error) {
	if err := p.settleLeftover(ctx, pub.ProjectID, pub.Branch); err != nil {
		return publishedResult{}, err
	}
	ref := "refs/heads/" + pub.Branch
	proposed, cleanup, err := stageProposal(ctx, pub)
	if err != nil {
		return publishedResult{}, err
	}
	defer cleanup()
	current := gitexec.HubRef(ctx, pub.Repository, ref)
	out := publishedResult{Previous: current, Tip: current}
	if pub.Expected != "" && pub.Expected != current {
		return out, &branchMoved{Branch: pub.Branch, Expected: pub.Expected, Found: current, Proposed: proposed}
	}
	if current != "" && proposed != current {
		ok := gitexec.HubAncestor(ctx, pub.Repository, current, proposed)
		if !ok && pub.Rewind {
			ok = gitexec.HubAncestor(ctx, pub.Repository, proposed, current)
		}
		if !ok {
			return out, &branchMoved{Branch: pub.Branch, Expected: current, Found: current, Proposed: proposed, NotForward: true}
		}
	}
	// A rewind to an ancestor brings nothing new and passes; permission to
	// rewind never exempts a move that adds commits.
	if proposed != current {
		if err := checkNewWork(ctx, pub.Repository, proposed, current); err != nil {
			return out, err
		}
	}
	id, err := p.begin(ctx, pub, current, proposed)
	if err != nil {
		return out, err
	}
	if proposed == current {
		if err := p.settle(ctx, id); err != nil {
			return out, fmt.Errorf("branch %s is at %s already, and its publication could not be settled: %w: %w", pub.Branch, strs.ShortSHA(proposed), ErrPublicationApplied, err)
		}
		return out, nil
	}
	txn, err := gitexec.PrepareUpdate(ctx, pub.Repository, ref, proposed, current)
	if err != nil {
		if settleErr := p.settle(ctx, id); settleErr != nil {
			return out, fmt.Errorf("%v; and settling the refused publication: %w", err, settleErr)
		}
		if errors.Is(err, gitexec.ErrTransactionRefused) {
			return out, &branchMoved{Branch: pub.Branch, Expected: current, Found: gitexec.HubRef(ctx, pub.Repository, ref), Proposed: proposed}
		}
		return out, err
	}
	if err := txn.Commit(); err != nil {
		// Git has exited (Commit waits for it), so observing the ref decides.
		row := scmsql.BranchPublication{ID: id, ProjectID: pub.ProjectID, Branch: pub.Branch, Repository: pub.Repository, Expected: current, Proposed: proposed}
		outcome, classifyErr := p.classify(ctx, row)
		switch {
		case outcome == published && classifyErr != nil:
			out.Tip, out.Changed = proposed, true
			return out, fmt.Errorf("branch %s moved, and its publication could not be settled; it is settled at next use: %w: %w", pub.Branch, ErrPublicationApplied, classifyErr)
		case classifyErr != nil || outcome == stillUnknown:
			return out, fmt.Errorf("publishing %s: %w: %v", pub.Branch, errPublicationUnknown, errors.Join(err, classifyErr))
		case outcome == abandoned:
			return out, fmt.Errorf("publishing %s: %w", pub.Branch, err)
		}
	} else if err := p.settle(ctx, id); err != nil {
		out.Tip, out.Changed = proposed, true
		return out, fmt.Errorf("branch %s moved, and its publication could not be settled; it is settled at next use: %w: %w", pub.Branch, ErrPublicationApplied, err)
	}
	out.Tip, out.Changed = proposed, true
	return out, nil
}

// stageProposal names the proposed commit: the commit Commit, or the one at
// Incoming, both already in the hub; or, for From, it brings the writer's
// branch into the hub under a private ref, so the objects are present
// before any branch moves. Git runs hardened in the hub (gitexec.HubRun).
func stageProposal(ctx context.Context, pub Publication) (string, func(), error) {
	switch {
	case pub.Commit != "":
		sha := gitexec.HubRef(ctx, pub.Repository, pub.Commit+"^{commit}")
		if sha == "" || sha != pub.Commit {
			return "", func() {}, fmt.Errorf("the commit %s is not in the project's repository", pub.Commit)
		}
		return sha, func() {}, nil
	case pub.Incoming != "":
		sha := gitexec.HubRef(ctx, pub.Repository, pub.Incoming)
		if sha == "" {
			return "", func() {}, fmt.Errorf("nothing was pushed to %s", pub.Incoming)
		}
		return sha, func() {}, nil
	}
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return "", func() {}, err
	}
	incoming := "refs/apptown/incoming/" + hex.EncodeToString(token)
	// The staged ref goes though the publication was cancelled.
	cleanup := func() {
		if out, err := gitexec.HubRun(context.WithoutCancel(ctx), pub.Repository, "update-ref", "-d", incoming); err != nil {
			log.Printf("removing staged publication ref %s: %v: %s", incoming, err, out)
		}
	}
	out, err := gitexec.HubRun(ctx, pub.Repository, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--end-of-options", pub.From, "+refs/heads/"+pub.Branch+":"+incoming)
	if err != nil {
		return "", func() {}, fmt.Errorf("publishing workflow branch %s: %w: %s", pub.Branch, err, publishDiagnostic(out))
	}
	sha := gitexec.HubRef(ctx, pub.Repository, incoming)
	if sha == "" {
		cleanup()
		return "", func() {}, fmt.Errorf("publishing %s: the staged commit is missing", pub.Branch)
	}
	return sha, cleanup, nil
}

func ancestor(ctx context.Context, repo, older, newer string) bool {
	// The answer has no way to say "unknown", so a stop must not shape it:
	// this bounded local read runs to its end though the caller stops.
	ctx = context.WithoutCancel(ctx)
	_, err := gitexec.Run(ctx, repo, "merge-base", "--is-ancestor", older, newer)
	return err == nil
}

// begin checks the grant and records the publication in one transaction.
func (p *Branches) begin(ctx context.Context, pub Publication, current, proposed string) (ids.ID, error) {
	return database.Transact(ctx, p.db, func(tx *database.Tx) (ids.ID, error) {
		authority, err := lockAuthorityIn(ctx, tx, pub.ProjectID, pub.Branch)
		if err != nil {
			return ids.Nil, err
		}
		q := scmsql.New(tx.SQL())
		if !pub.Grant.admits(authority) {
			return ids.Nil, errAuthoritySuperseded
		}
		if pub.Grant.Valid != nil {
			if err := pub.Grant.Valid(ctx, tx); err != nil {
				return ids.Nil, err
			}
		}
		id, err := q.BeginPublication(ctx, scmsql.BeginPublicationParams{ID: ids.New(p.now()), ProjectID: pub.ProjectID, Branch: pub.Branch, Repository: pub.Repository,
			Generation: authority.Generation, Expected: current, Proposed: proposed, CreatedAt: p.now()})
		if errors.Is(err, sql.ErrNoRows) {
			return ids.Nil, errPublicationUnsettled
		}
		return id, err
	})
}

func (p *Branches) settle(ctx context.Context, id ids.ID) error {
	_, err := scmsql.New(p.db.SQL()).SettlePublication(ctx, id)
	return err
}

type publicationOutcome uint8

const (
	stillUnknown publicationOutcome = iota
	published
	abandoned
)

// classify settles a recorded publication by observing the branch. While the
// branch's lock file exists a git process may still move it, so the
// publication stays unknown. Once it is gone, the branch value is final.
func (p *Branches) classify(ctx context.Context, row scmsql.BranchPublication) (publicationOutcome, error) {
	// A repository whose lock cannot be checked (gone, unreadable, another
	// ref format) leaves the outcome unknown: the publication stays recorded
	// and holds its branch, and the other publications are still settled.
	locked, err := refLocked(ctx, row.Repository, "refs/heads/"+row.Branch)
	if err != nil {
		log.Printf("publication of branch %s in %s: %v; its outcome stays unknown", row.Branch, row.Repository, err)
	}
	outcome := stillUnknown
	if err == nil && !locked {
		// Read hardened and error-aware: a failed read leaves the outcome
		// unknown, and is never taken as a branch that does not exist (an
		// abandoned first publication).
		tip, err := gitexec.HubReadRef(ctx, row.Repository, "refs/heads/"+row.Branch)
		if err != nil {
			log.Printf("publication of branch %s in %s: %v; its outcome stays unknown", row.Branch, row.Repository, err)
		} else {
			switch tip {
			case row.Proposed:
				outcome = published
			case row.Expected:
				outcome = abandoned
			}
		}
	}
	q := scmsql.New(p.db.SQL())
	if outcome == stillUnknown {
		if _, err := q.MarkPublicationUnknown(ctx, row.ID); err != nil {
			return stillUnknown, err
		}
		return stillUnknown, nil
	}
	_, err = q.SettlePublication(ctx, row.ID)
	return outcome, err
}

// settleLeftover settles an earlier publication of the branch, if one is
// recorded, before another starts.
func (p *Branches) settleLeftover(ctx context.Context, projectID ids.ID, branch string) error {
	row, err := scmsql.New(p.db.SQL()).PublicationForBranch(ctx, scmsql.PublicationForBranchParams{ProjectID: projectID, Branch: branch})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	outcome, err := p.classify(ctx, row)
	if err != nil {
		return err
	}
	if outcome == stillUnknown {
		return errPublicationUnsettled
	}
	return nil
}

// Settle settles the branch's recorded publication, if one is left, by
// observing the branch, as its next publication would. A publication whose
// outcome is still unknown stays recorded (ErrPublicationUnsettled).
func (p *Branches) Settle(ctx context.Context, projectID ids.ID, branch string) error {
	release, err := p.locks.Acquire(ctx, projectID)
	if err != nil {
		return err
	}
	defer release()
	return p.settleLeftover(ctx, projectID, branch)
}

// refLocked reports whether git holds ref's lock in the repository. Only the
// files ref backend is supported: its lock file is the ref path plus ".lock".
func refLocked(ctx context.Context, repo, ref string) (bool, error) {
	if format, err := gitexec.HubRun(ctx, repo, "rev-parse", "--show-ref-format"); err == nil && format != "" && format != "files" {
		return false, fmt.Errorf("branch publication supports the files ref format, not %q", format)
	}
	gitDir, err := gitexec.HubRun(ctx, repo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false, fmt.Errorf("locating %s's git directory: %w", repo, err)
	}
	_, err = os.Stat(filepath.Join(gitDir, filepath.FromSlash(ref)+".lock"))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

type publicationRecovery struct{ Inspected, Settled, Unknown int }

// Recover settles publications left by a previous process. It runs at startup
// before work is dispatched. A publication that cannot be settled stays
// recorded and holds its branch; an operator resolves it.
func (p *Branches) Recover(ctx context.Context) (publicationRecovery, error) {
	var report publicationRecovery
	rows, err := scmsql.New(p.db.SQL()).UnsettledPublications(ctx)
	if err != nil {
		return report, err
	}
	for _, row := range rows {
		report.Inspected++
		outcome, err := p.classify(ctx, row)
		if err != nil {
			return report, fmt.Errorf("inspecting the publication of %s: %w", row.Branch, err)
		}
		if outcome == stillUnknown {
			report.Unknown++
			at, readErr := gitexec.HubReadRef(ctx, row.Repository, "refs/heads/"+row.Branch)
			if readErr != nil {
				at = "unreadable (" + readErr.Error() + ")"
			} else {
				at = strs.ShortSHA(at)
			}
			log.Printf("startup repair: publication of branch %s in %s is unknown (the branch is at %s, locked or neither %s nor %s); it holds the branch until resolved",
				row.Branch, row.Repository, at, strs.ShortSHA(row.Expected), strs.ShortSHA(row.Proposed))
			continue
		}
		report.Settled++
	}
	return report, nil
}
