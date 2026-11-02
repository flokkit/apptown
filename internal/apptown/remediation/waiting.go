package remediation

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Waiting explains a queued item's current hold, rather than its last outcome.
// Change and remediation identities let readers link the work that occupies it.
type Waiting struct {
	Kind              string                    `json:"kind"`
	Reason            string                    `json:"reason"`
	Changes           []workflow.ProjectBlocker `json:"changes,omitempty"`
	RemediationID     ids.ID                    `json:"remediationId,omitzero"`
	RemediationNumber int64                     `json:"remediationNumber,omitempty"`
}

// Admission, scheduling and page reads share this occupancy decision. A
// run waiting on a person does not occupy the project.
func projectWaitIn(ctx context.Context, db database.Queryer, project ids.ID, slug string) (*Waiting, error) {
	active, err := queries(db).ProjectActiveItems(ctx, project)
	if err != nil {
		return nil, err
	}
	if len(active) > 0 {
		a := active[0]
		return &Waiting{Kind: "remediation", Reason: fmt.Sprintf("Waiting: %s has another item in progress in remediation %d.", slug, a.Number), RemediationID: a.RemediationID, RemediationNumber: a.Number}, nil
	}
	blockers, err := workflow.ProjectBlockersIn(ctx, db, project)
	if err != nil {
		return nil, err
	}
	if len(blockers) == 0 {
		return nil, nil
	}
	wait := &Waiting{Kind: "project", Reason: "Waiting: a step is running on " + slug + "."}
	var names []string
	for _, blocker := range blockers {
		if blocker.ID.IsZero() {
			continue
		}
		wait.Changes = append(wait.Changes, blocker)
		if blocker.Kind == "landing" {
			names = append(names, fmt.Sprintf("change %d is landing", blocker.Number))
		} else {
			names = append(names, fmt.Sprintf("a step is running in change %d", blocker.Number))
		}
	}
	if len(names) > 0 {
		named := strings.Join(names, ", ")
		if len(names) > 1 {
			named = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
		}
		wait.Reason = fmt.Sprintf("Waiting: %s.", named)
	}
	return wait, nil
}

// waitingItemsIn reads current explanations from the same snapshot as the job.
// Stored retry feedback remains intact; waiting is only a read projection.
func (s *Service) waitingItemsIn(ctx context.Context, tx *database.Tx, job Remediation, items []Item) error {
	at := s.cool.now()
	until, why := s.cool.state()
	holds := map[ids.ID]*Waiting{}
	var atLimit *bool
	for i := range items {
		it := &items[i]
		if it.State != ItemQueued {
			continue
		}
		wait := &Waiting{Kind: "worker", Reason: "Waiting: starts on the next pass when a worker is free."}
		switch {
		case job.Status != RemRunning:
			wait = &Waiting{Kind: "paused", Reason: "Waiting: this remediation is " + strings.ReplaceAll(job.Status, "_", " ") + "."}
		default:
			scope := it.ProjectID
			occupied, read := holds[scope]
			if !read {
				var err error
				occupied, err = projectWaitIn(ctx, tx, it.ProjectID, it.ProjectSlug)
				if err != nil {
					return err
				}
				holds[scope] = occupied
			}
			switch {
			case occupied != nil:
				wait = occupied
			case it.NotBefore.Valid && !it.NotBefore.PassedAt(at):
				wait = &Waiting{Kind: "retry", Reason: "Waiting: a retry is delayed. " + it.Reason}
			case at.Before(until):
				wait = &Waiting{Kind: "cooldown", Reason: "Waiting: the worker pool is cooling down. " + why}
			case job.Concurrency > 0:
				if atLimit == nil {
					counts, err := queries(tx).ActiveItemCounts(ctx, job.ID)
					if err != nil {
						return err
					}
					full := int(counts.InJob) >= job.Concurrency
					atLimit = &full
				}
				if *atLimit {
					wait = &Waiting{Kind: "concurrency", Reason: "Waiting: this remediation is at its worker limit."}
				}
			}
		}
		it.Waiting = wait
	}
	return nil
}

func addWaitingCounts(job *Remediation, items []Item) {
	counts := map[string]int{}
	for _, it := range items {
		if it.Waiting != nil {
			counts[it.Waiting.Kind]++
		}
	}
	for _, group := range []struct{ kind, label string }{
		{"project", "waiting on project work"}, {"remediation", "waiting on another remediation item"},
		{"cooldown", "waiting on pool cooldown"}, {"retry", "waiting to retry"}, {"paused", "waiting on this remediation"},
		{"concurrency", "waiting on the remediation worker limit"}, {"worker", "waiting for the next pass or a free worker"},
	} {
		if n := counts[group.kind]; n > 0 {
			job.Sentence += fmt.Sprintf(" · %d %s", n, group.label)
		}
	}
}
