package workcommands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
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

// publishForLanding publishes the change folder's committed work before a
// landing pins its candidate, under the holder's own grant: the holding
// chat's, or when nobody holds the change, a grant the request takes and
// releases at once. A run or a checkout holding the change publishes its
// own work; nothing is published for it here. It reports what the folder
// has not committed.
func (a *Preparation) publishForLanding(ctx context.Context, p *project.Project, ch *workflow.Change) ([]string, error) {
	if ch.Branch == "" {
		return nil, nil
	}
	authority, err := scm.ReadAuthorityIn(ctx, a.db, p.ID, ch.Branch)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(authority.Holder, "chat:") {
		ended, err := workflow.HolderRecordsEndedIn(ctx, a.db, p.ID, authority.Holder, a.chats, a.now())
		if err != nil {
			return nil, err
		}
		if !ended {
			// A chat on a worker has its work there: it is saved (and the
			// change's folder follows the branch) before the landing
			// chooses what it lands.
			if a.workers != nil {
				if onWorker, err := a.workers.SaveFirst(ctx, authority.Holder); onWorker || err != nil {
					return nil, err
				}
			}
			return a.PublishChangeFolder(ctx, p, ch, authority.Holder)
		}
	}
	holder, release, err := workflow.TakeRequestIn(ctx, a.db, p.ID, ch.Branch, "landing", a.chats, a.now())
	var held *workflow.ChangeHeld
	if errors.As(err, &held) {
		return nil, nil // the landing's admission refuses it
	}
	if err != nil {
		return nil, err
	}
	uncommitted, published := a.PublishChangeFolder(ctx, p, ch, holder)
	if err := release(ctx); err != nil {
		return nil, errors.Join(published, err)
	}
	return uncommitted, published
}

// PublishForLanding publishes the change's committed work before a landing
// is rechecked, as its admission does.
func (a *Preparation) PublishForLanding(ctx context.Context, ch *workflow.Change) error {
	p, err := project.ByIDIn(ctx, a.db, ch.ProjectID)
	if err != nil {
		return err
	}
	_, err = a.publishForLanding(ctx, &p, ch)
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
