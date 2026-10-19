package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"flokkit.io/forge/internal/database"
)

var interpRe = regexp.MustCompile(`\$\{\{\s*([a-zA-Z]+)\.([a-zA-Z0-9_-]+)\s*\}\}`)

// Interpolate expands ${{change.*}} (alias ${{workItem.*}}), ${{run.*}},
// ${{project.*}} and
// ${{artifact.<kind>}} (latest artifact body of that kind on this run).
func Interpolate(ctx context.Context, q database.Queryer, s string, run *Run, item *Change, projectSlug string) string {
	return interpRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := interpRe.FindStringSubmatch(m)
		ns, key := parts[1], parts[2]
		switch ns {
		case "workItem", "change":
			if item == nil {
				return m
			}
			switch key {
			case "title":
				return item.Title
			case "body":
				return item.Body
			case "type":
				return item.Type
			case "id":
				return fmt.Sprintf("%d", item.ID)
			}
		case "run":
			switch key {
			case "id":
				return fmt.Sprintf("%d", run.ID)
			case "stage":
				return run.CurrentStage
			case "workflow":
				return run.WorkflowName
			}
		case "project":
			if key == "slug" {
				return projectSlug
			}
		case "inputs":
			// ${{inputs.*}}: parameters a workflow/call parent passed in
			// (frozen at invocation; composition.md §2).
			var inputs map[string]string
			if json.Unmarshal([]byte(run.InputsJSON), &inputs) == nil {
				if v, ok := inputs[key]; ok {
					return v
				}
			}
			return "(no input \"" + key + "\" passed to this run)"
		case "artifact":
			reports, err := ReadRunArtifactsIn(ctx, q, RunRef{ID: run.ID, ProjectID: run.ProjectID}, ArtifactSelection{Kinds: []string{key}, Latest: true})
			if _, err := database.Lookup(err); err != nil {
				log.Printf("read failed: %v", err)
			}
			if err == nil && len(reports) > 0 {
				return reports[0].Body
			}
			return "(no artifact of kind \"" + key + "\" on this run yet)"
		case "feedback":
			// ${{feedback.latest}}: the most recent rejection on this run —
			// a reviewer's words, an approver's comment, or a failed boot
			// gate's crash log — fed to the agent on re-run. This is the
			// rejection loop: reject → re-run producing step → re-verify.
			feedback, err := LatestFeedbackIn(ctx, q, RunRef{ID: run.ID, ProjectID: run.ProjectID})
			if _, err := database.Lookup(err); err != nil {
				log.Printf("read failed: %v", err)
			}
			if err == nil && feedback != nil {
				var ev struct {
					LastRejection *ApprovalRejection  `json:"lastRejection"`
					Withdrawal    *ApprovalWithdrawal `json:"withdrawal"`
					Comment       string              `json:"comment"`
					Approver      string              `json:"approver"`
					Summary       string              `json:"summary"`
					Findings      []string            `json:"findings"`
					Reason        string              `json:"reason"`
					LogTail       string              `json:"logTail"`
					// Set when the forge's file check failed a review the model passed.
					ReviewerSummary string `json:"reviewerSummary"`
				}
				if json.Unmarshal(feedback.Evidence, &ev) == nil {
					if ev.Summary != "" { // agent/review rejection
						fb := ev.Summary
						if len(ev.Findings) > 0 {
							fb += "\nFindings:\n- " + strings.Join(ev.Findings, "\n- ")
						}
						if ev.LogTail != "" { // a review not run because the boot failed carries the boot's log
							fb += "\n--- container log tail ---\n" + ev.LogTail
						}
						if ev.ReviewerSummary != "" {
							return fb + " (— AppTown's file check; the reviewer itself passed the work)"
						}
						return fb + " (— automated reviewer)"
					}
					if ev.Withdrawal != nil {
						return fmt.Sprintf("acceptance withdrawn: %s (— %s)", ev.Withdrawal.Reason, ev.Withdrawal.By)
					}
					if ev.LastRejection != nil && ev.Comment == "" {
						return fmt.Sprintf("%s (— %s)", ev.LastRejection.Comment, ev.LastRejection.Approver)
					}
					if ev.Comment != "" {
						return fmt.Sprintf("%s (— %s)", ev.Comment, ev.Approver)
					}
					if ev.Reason != "" { // boot gate: the app's own crash is the feedback
						fb := "the app failed its cold boot gate: " + ev.Reason
						if ev.LogTail != "" {
							fb += "\n--- container log tail ---\n" + ev.LogTail
						}
						return fb + "\n(— boot gate; fix the contract or the app so a COLD container can build and serve it)"
					}
				}
			}
			return "(no review feedback on this run)"
		case "latest":
			// Latest artifact of this kind across the whole project — the
			// cross-run reference (e.g. an implement run consuming the spec
			// accepted by an earlier product-spec run).
			reports, err := ReadProjectArtifactsIn(ctx, q, run.ProjectID, ArtifactSelection{Kinds: []string{key}, Latest: true})
			if _, err := database.Lookup(err); err != nil {
				log.Printf("read failed: %v", err)
			}
			if err == nil && len(reports) > 0 {
				return reports[0].Body
			}
			return "(no artifact of kind \"" + key + "\" in this project yet)"
		}
		return m
	})
}
