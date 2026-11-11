package workcommands

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/trailer"
)

// PublishChangeFolder publishes what the change's folder has committed on
// its branch, under holder's own grant, and reports the files it has not
// committed, which stay in the folder and are not included. It is how a
// chat's work reaches the branch: when a landing it asks for needs it, and
// when the chat ends. The agent commits what is ready; nothing is committed
// for it here.
// A branch that moved elsewhere, or a holder that no longer holds it,
// refuses the publication; the folder keeps its work.
func (a *Preparation) PublishChangeFolder(ctx context.Context, p *project.Project, ch *workflow.Change, holder string) ([]string, error) {
	ws := (workflow.Layout{DataDir: a.dataDir}).ChangeWorkspace(ch.ID)
	if !gitexec.IsRepo(ws) || ch.Branch == "" {
		return nil, nil
	}
	status, err := gitexec.Output(ctx, ws, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("reading the change's folder: %w", err)
	}
	var uncommitted []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) > 3 && !scm.RuntimeOnly(line[3:]) {
			uncommitted = append(uncommitted, line[3:])
		}
	}
	hub, err := p.Repository(a.dataDir)
	if err != nil {
		return nil, err
	}
	tip := scm.Ref(ctx, ws, "refs/heads/"+ch.Branch)
	if tip == "" {
		return uncommitted, nil
	}
	if _, err := gitexec.Run(ctx, hub, "merge-base", "--is-ancestor", tip, "refs/heads/"+ch.Branch); err == nil {
		return uncommitted, nil // published already
	}
	if _, err := a.branches.Publish(ctx, scm.Publication{ProjectID: p.ID, Repository: hub, Branch: ch.Branch, From: ws, Grant: scm.Grant{Holder: holder}}); err != nil {
		return uncommitted, fmt.Errorf("publishing change %d's committed work: %w", ch.ID, err)
	}
	return uncommitted, nil
}

// landingPublication is what publishing a change for its landing did: the
// files its folder has not committed, and main merged in, when it was.
type landingPublication struct {
	uncommitted []string
	merged      *scm.MainMerge
}

// publishForLanding publishes the change folder's committed work before a
// landing pins its candidate, under the holder's own grant: the holding
// chat's, or when nobody holds the change, a grant the request takes and
// releases at once. A run or a checkout holding the change publishes its
// own work; nothing is published for it here. Under the same grant it
// then brings main in (catchUp), so the landing checks the merged tree. by
// is who asked, as the landing records them.
func (a *Preparation) publishForLanding(ctx context.Context, p *project.Project, ch *workflow.Change, by string) (landingPublication, error) {
	if ch.Branch == "" {
		return landingPublication{}, nil
	}
	authority, err := scm.ReadAuthorityIn(ctx, a.db, p.ID, ch.Branch)
	if err != nil {
		return landingPublication{}, err
	}
	if strings.HasPrefix(authority.Holder, "chat:") {
		ended, err := workflow.HolderRecordsEndedIn(ctx, a.db, p.ID, authority.Holder, a.chats, a.now())
		if err != nil {
			return landingPublication{}, err
		}
		if !ended {
			// A chat on a worker has its work there: it is saved (and the
			// change's folder follows the branch) before the landing
			// chooses what it lands.
			if a.workers != nil {
				onWorker, err := a.workers.SaveFirst(ctx, authority.Holder)
				if err != nil {
					return landingPublication{}, err
				}
				if onWorker {
					return a.catchUp(ctx, p, ch, nil, by, authority.Holder, true)
				}
			}
			uncommitted, err := a.PublishChangeFolder(ctx, p, ch, authority.Holder)
			if err != nil {
				return landingPublication{uncommitted: uncommitted}, err
			}
			return a.catchUp(ctx, p, ch, uncommitted, by, authority.Holder, false)
		}
	}
	holder, release, err := workflow.TakeRequestIn(ctx, a.db, p.ID, ch.Branch, "landing", a.chats, a.now())
	var held *workflow.ChangeHeld
	if errors.As(err, &held) {
		return landingPublication{}, nil // the landing's admission refuses it
	}
	if err != nil {
		return landingPublication{}, err
	}
	out, published := a.PublishChangeFolder(ctx, p, ch, holder)
	result := landingPublication{uncommitted: out}
	if published == nil {
		result, published = a.catchUp(ctx, p, ch, out, by, holder, false)
	}
	if err := release(ctx); err != nil {
		return landingPublication{}, errors.Join(published, err)
	}
	return result, published
}

// catchUp brings main into the change's branch for its landing, under
// holder's grant, when the branch is behind main and the merge has no
// conflicts: AppTown's merge commit (scm.MergeMain), naming who asked. A
// merge with conflicts is not made; the landing's first step ends the
// landing with the files, and the change goes back to its chat. The change
// page asks the same question before anyone presses, and offers no
// landing then.
//
// Work left uncommitted with nothing committed is refused first, never
// landed as an empty change (star-hop shipped an empty change when its
// work was left uncommitted). It is decided on the branch before any
// merge: a merge of the fork point and a moved main has main's tree but is
// not in main. A change with no work and a clean folder lands as before.
//
// The holder's copy of the work follows the merge. A chat on a worker
// takes it into its work tree before it is published (its saves bundle
// from the branch's tip, which it must hold); one that cannot refuses the
// landing and nothing moves. Otherwise the merge is published and the
// change's folder fast-forwarded; a folder with files in the way stays
// behind, the branch holds the merge, and a chat working there is told how
// to take it.
func (a *Preparation) catchUp(ctx context.Context, p *project.Project, ch *workflow.Change, uncommitted []string, by, holder string, onWorker bool) (landingPublication, error) {
	out := landingPublication{uncommitted: uncommitted}
	if p.DefaultBranch == "" {
		return out, nil
	}
	hub, err := p.Repository(a.dataDir)
	if err != nil {
		return out, err
	}
	tip, main := scm.Ref(ctx, hub, "refs/heads/"+ch.Branch), scm.Ref(ctx, hub, "refs/heads/"+p.DefaultBranch)
	if len(uncommitted) > 0 && (tip == "" || (main != "" && scm.Contains(ctx, hub, p.DefaultBranch, tip))) {
		return out, fault.Wrap(fault.Conflict, fmt.Errorf("nothing to land: change %d has no committed work beyond %s; commit it first (uncommitted: %s)", ch.ID, p.DefaultBranch, strings.Join(uncommitted, ", ")))
	}
	if tip == "" || main == "" {
		return out, nil
	}
	message := trailer.Message(repositoryedit.MergeTitle(p.DefaultBranch, ch.Branch),
		fmt.Sprintf("AppTown merged %[1]s into this change when %[2]s asked to land it: the branch was behind %[1]s and the merge had no conflicts.", p.DefaultBranch, by),
		trailer.Line{Key: trailer.ChangeID, Value: ch.ID.String()}, trailer.Line{Key: trailer.Actor, Value: by}) + "\n"
	merge, err := scm.MergeMain(ctx, hub, tip, p.DefaultBranch, message, a.now())
	if err != nil {
		return out, fmt.Errorf("merging %s into change %d for its landing: %w", p.DefaultBranch, ch.Number, err)
	}
	if merge.Commit == "" {
		// Nothing to merge, or main conflicts: the landing's first step
		// then ends it with the files, which the change page shows; the
		// page offers no landing while they conflict.
		return out, nil
	}
	ws := (workflow.Layout{DataDir: a.dataDir}).ChangeWorkspace(ch.ID)
	publish := func() error {
		// The change's views read the branch while the folder lacks the
		// merge (scm.Behind), as for a chat's save.
		if err := scm.MarkSaved(ctx, ws, hub, ch.Branch, merge.Commit); err != nil {
			return fmt.Errorf("recording the merge for change %d's folder: %w", ch.Number, err)
		}
		if _, err := a.branches.Publish(ctx, scm.Publication{ProjectID: p.ID, Repository: hub, Branch: ch.Branch, Commit: merge.Commit, Expected: tip, Grant: scm.Grant{Holder: holder}}); err != nil {
			return fmt.Errorf("publishing the merge of %s into change %d: %w", p.DefaultBranch, ch.Number, err)
		}
		return nil
	}
	tell := func(text string) {
		if a.workers != nil && strings.HasPrefix(holder, "chat:") {
			a.workers.Tell(ctx, holder, text)
		}
	}
	chat := strings.HasPrefix(holder, "chat:")
	if onWorker {
		if err := a.workers.FollowFirst(ctx, holder, hub, tip, merge.Commit, publish); err != nil {
			return out, fault.Wrap(fault.Conflict, fmt.Errorf("%[1]s could not be merged into change %[2]d for its landing: the chat's folder could not take the merge: %[3]w. Catch up with %[1]s in the chat (%[4]s), then land again",
				p.DefaultBranch, ch.Number, err, repositoryedit.CatchUpCommand(p.DefaultBranch, ch.Branch)))
		}
		tell(fmt.Sprintf("[apptown] %s was merged into this change for its landing (commit %s); your folder has it.", p.DefaultBranch, strs.ShortSHA(merge.Commit)))
	} else {
		if err := publish(); err != nil {
			return out, err
		}
		if gitexec.IsRepo(ws) {
			held := gitexec.LockFolder(ws)
			followed := held.FastForward(ctx, hub, ch.Branch)
			held.Release()
			switch {
			case followed != nil && chat:
				tell(fmt.Sprintf("[apptown] %[1]s was merged into this change's branch for its landing (commit %[2]s), but your folder could not take it: %[3]v. Commit or set aside the files in the way, then run: git merge --ff-only %[2]s",
					p.DefaultBranch, merge.Commit, followed))
			case followed != nil:
				log.Printf("change %d: its folder could not take the merge of %s; the branch holds it: %v", ch.ID, p.DefaultBranch, followed)
			case chat:
				tell(fmt.Sprintf("[apptown] %s was merged into this change for its landing (commit %s); your folder has it.", p.DefaultBranch, strs.ShortSHA(merge.Commit)))
			}
		}
	}
	out.merged = &merge
	return out, nil
}

// mainMerged is the landing's record of main merged in for it.
func mainMerged(ch *workflow.Change, mainBranch, by, step string, merge *scm.MainMerge) map[string]any {
	return map[string]any{"by": by, "step": step, "change": ch.ID, "branch": ch.Branch, "mainBranch": mainBranch, "main": merge.Main, "sha": merge.Commit}
}

// PublishForLanding publishes the change's committed work, and brings main
// in, before a landing is rechecked, as its admission does; main merged in
// is recorded on the landing's run.
func (a *Preparation) PublishForLanding(ctx context.Context, ch *workflow.Change, by string, runID ids.ID) error {
	p, err := project.ByIDIn(ctx, a.db, ch.ProjectID)
	if err != nil {
		return err
	}
	published, err := a.publishForLanding(ctx, &p, ch, by)
	if err == nil && published.merged != nil && !runID.IsZero() {
		workflow.RecordNoticeOrLog(context.WithoutCancel(ctx), a.db, runID, repositoryedit.MainMergedNotice, mainMerged(ch, p.DefaultBranch, by, "recheck", published.merged), a.now())
	}
	return err
}

// admissionCandidate pins what a landing is admitted with: the change's
// branch as published (candidate), and the default branch (target). Either
// missing pins nothing yet.
func admissionCandidate(candidate, target string) (*workflow.StartEvent, error) {
	if candidate == "" || target == "" {
		return nil, nil
	}
	event, err := workflow.LandingCandidateStart(workflow.LandingCandidate{Candidate: candidate, Target: target, By: "admission"})
	return &event, err
}
