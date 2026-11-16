package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
)

// fixableTargets observes settled named failures in this execution's stage.
// Scanner errors cannot supply targets. Retained attempt targets survive purge.
func (e *Execution) fixableTargets(ctx context.Context, names []string, ds []fix.Declaration) ([]check.Finding, string, error) {
	steps, err := readStageSteps(ctx, e.service.db, e.run.ID, e.step.Stage)
	if err != nil {
		return nil, "", err
	}
	heads, err := conformance.ScanObservationHeadsIn(ctx, e.service.db, e.run.ProjectID)
	if err != nil {
		return nil, "", err
	}
	selected := make([]ids.ID, 0, len(heads))
	for _, h := range heads {
		selected = append(selected, h.ID)
	}
	observations, err := conformance.ScanObservationsByIDIn(ctx, e.service.db, e.run.ProjectID, selected)
	if err != nil {
		return nil, "", err
	}
	history, err := workflowsql.New(e.service.db.SQL()).PreviousHeldAttempt(ctx, workflowsql.PreviousHeldAttemptParams{ProjectID: e.run.ProjectID, ChangeID: e.run.ChangeID})
	if err != nil {
		return nil, "", err
	}
	previous := map[string]bool{}
	if len(history) > 0 {
		var ts []check.Finding
		if err := json.Unmarshal(history[0].Targets, &ts); err != nil {
			return nil, "", err
		}
		for _, t := range ts {
			previous[t.Fingerprint] = true
		}
	}
	stopped := false
	current, err := workflowsql.New(e.service.db.SQL()).QueueAttempt(ctx, e.run.ChangeID)
	if err != nil {
		return nil, "", err
	}
	if len(current) > 0 {
		attempts, err := queueEpisodeIn(ctx, e.service.db, e.run.ProjectID, current[0].Queue)
		if err != nil {
			return nil, "", err
		}
		failed := 0
		for _, attempt := range attempts {
			if attempt.FailedAttempt && !attempt.promoted {
				failed++
			}
		}
		stopped = failed >= queueStopAfter
		if stopped {
			for _, attempt := range attempts {
				if attempt.promoted {
					break
				}
				if attempt.Outcome == "faulted" {
					var targets []check.Finding
					if err := json.Unmarshal(attempt.Targets, &targets); err != nil {
						return nil, "", err
					}
					for _, target := range targets {
						previous[target.Fingerprint] = true
					}
				}
			}
		}
	}

	var publicPart string
	var publicPartRead bool
	seen := map[string]bool{}
	var targets []check.Finding
	for _, name := range names {
		found := false
		for _, s := range steps {
			if s.Name != name && !strings.HasPrefix(s.Name, name+":") {
				continue
			}
			found = true
			if s.Status == StatusPending || s.Status == StatusRunning {
				return nil, "", fmt.Errorf("step %s has not settled", s.Name)
			}
			if s.Status != StatusFail {
				continue
			}
			var cfg struct {
				FailOn string `json:"failOn"`
			}
			if err := json.Unmarshal(s.Config, &cfg); err != nil {
				return nil, "", err
			}
			for _, o := range observations {
				if o.RunID != e.run.ID || o.StepID != s.ID || o.Attempt != s.Attempt || (o.Subject.On != "source" && o.Subject.On != "running") {
					continue
				}
				for _, f := range o.Findings {
					if f.Package == nil || previous[f.Fingerprint] || seen[f.Fingerprint] || stopped && !check.FailsAt(cfg.FailOn, f.Severity) {
						continue
					}
					image := o.Subject.On == "running"
					if image {
						if !check.FailsAt(cfg.FailOn, f.Severity) {
							continue
						}
						if !publicPartRead {
							source, err := ReadSourceIn(ctx, e.service.db, RunRef{ID: e.run.ID, ProjectID: e.run.ProjectID})
							if err != nil {
								return nil, "", err
							}
							repo, err := source.Repository(e.service.dataDir)
							if err != nil {
								return nil, "", err
							}
							contract, err := docker.ReadContractAt(ctx, repo, source.DefaultBranch)
							if err != nil {
								return nil, "", err
							}
							// Static parts run AppTown's file server, not an image the project can repair.
							if part := contract.PublicPart(); part != nil && !part.IsStatic() {
								publicPart = part.Name
							}
							publicPartRead = true
						}
						if publicPart == "" || publicPart != o.Subject.Part {
							continue
						}

						shared := false
						for _, scan := range observations {
							if scan.RunID != e.run.ID || scan.Subject.On != "source" {
								continue
							}
							if !slices.ContainsFunc(steps, func(step *Step) bool { return step.ID == scan.StepID && step.Attempt == scan.Attempt }) {
								continue
							}
							for _, sf := range scan.Findings {
								if samePackageFinding(f.Finding, sf.Finding) {
									shared = true
								}
							}
						}
						if shared {
							continue
						}
					}
					if image || slices.ContainsFunc(ds, func(d fix.Declaration) bool { return fix.SupportsTargets(d) && d.Ecosystem == f.Package.Ecosystem }) {
						target := f.Finding
						target.ScanPoint = "source"
						if image {
							target.ScanPoint = "image"
						}
						targets = append(targets, target)
						seen[f.Fingerprint] = true
					}
				}
			}
		}
		if !found {
			return nil, "", fmt.Errorf("whenFailed names unknown step %s", name)
		}
	}
	if len(targets) == 0 {
		if len(history) > 0 {
			return nil, "nothing new since " + history[0].OpenedAt.Format("2 Jan"), nil
		}
		return nil, "no failed step has a fixable finding", nil
	}
	return targets, "", nil
}
