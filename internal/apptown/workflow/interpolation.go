package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

var interpRe = regexp.MustCompile(`\$\{\{\s*([a-zA-Z]+)\.([a-zA-Z0-9_-]+)\s*\}\}`)

// Interpolate expands ${{change.*}} (alias ${{workItem.*}}), ${{run.*}},
// ${{project.*}} and
// ${{artifact.<kind>}} (latest artifact body of that kind on this run) and
// ${{preview.url}} / ${{preview.host}} (this run's latest recorded preview).
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
		case "preview":
			if key == "url" || key == "host" {
				return previewValue(ctx, q, run, key)
			}
		case "feedback":
			// The check that requested the revision, or a person's rejection.
			// Runners keep their own evidence shape; one renderer serves them all.
			feedback, err := latestFeedbackIn(ctx, q, RunRef{ID: run.ID, ProjectID: run.ProjectID})
			if _, err := database.Lookup(err); err != nil {
				log.Printf("read failed: %v", err)
			}
			if err == nil && feedback != nil {
				return renderFeedback(feedback)
			}
			return "(no review feedback on this run)"
		case "latest":
			// Latest artifact of this kind across the whole project — the
			// cross-run reference (e.g. an implement run consuming the spec
			// accepted by an earlier product-spec run).
			reports, err := readProjectArtifactsIn(ctx, q, run.ProjectID, ArtifactSelection{Kinds: []string{key}, Latest: true})
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

// previewValue is ${{preview.url}} or ${{preview.host}}: the address the
// run's preview step recorded last (its artifact's ref; a record, not a
// check that the preview still answers). host is the URL's authority with
// its port, because AppTown forwards the Host unchanged and an app builds
// its links from it (the trial's change-….preview.localhost:7777). A
// reviewer reaches the preview by sending that Host to APPTOWN_URL, which
// routes by host name only when the preview has a preview label as its
// first name; a direct published-port URL has none, and the sentence says
// so rather than handing out a Host that reaches AppTown itself.
func previewValue(ctx context.Context, q database.Queryer, run *Run, key string) string {
	previews, err := ReadRunArtifactsIn(ctx, q, RunRef{ID: run.ID, ProjectID: run.ProjectID}, ArtifactSelection{Kinds: []string{"preview"}, Latest: true})
	if _, lookupErr := database.Lookup(err); lookupErr != nil {
		log.Printf("read failed: %v", lookupErr)
		return "AppTown could not read this run's preview URL."
	}
	if err != nil || len(previews) == 0 || previews[0].Ref == "" {
		return "No preview URL has been recorded for this run."
	}
	address := previews[0].Ref
	if key == "url" {
		return address
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return "AppTown could not read this run's preview URL."
	}
	label, _, _ := strings.Cut(u.Hostname(), ".")
	if !docker.IsWorkPreviewLabel(label) {
		return "This preview is not host routed; the console Host-header method cannot open it."
	}
	return u.Host
}

// Evidence belongs to its runner. Render its complete JSON rather than assuming
// one runner's schema (scan findings, for example, are an object).
func renderFeedback(step *Step) string {
	var ev struct {
		LastRejection *ApprovalRejection  `json:"lastRejection"`
		Withdrawal    *ApprovalWithdrawal `json:"withdrawal"`
		Comment       string              `json:"comment"`
		Approver      string              `json:"approver"`
	}
	if json.Unmarshal(step.Evidence, &ev) == nil {
		if ev.Withdrawal != nil {
			return fmt.Sprintf("acceptance withdrawn: %s (— %s)", ev.Withdrawal.Reason, ev.Withdrawal.By)
		}
		if ev.LastRejection != nil && ev.Comment == "" {
			return fmt.Sprintf("%s (— %s)", ev.LastRejection.Comment, ev.LastRejection.Approver)
		}
		if ev.Comment != "" {
			return fmt.Sprintf("%s (— %s)", ev.Comment, ev.Approver)
		}
	}
	return fmt.Sprintf("Feedback from %s (%s):\n%s", step.Name, step.Type, renderEvidence(step.Evidence))
}

func evidenceReason(raw json.RawMessage) string {
	var ev struct {
		Reason  string `json:"reason"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal(raw, &ev) == nil {
		if ev.Reason != "" {
			return ev.Reason
		}
		if ev.Summary != "" {
			return ev.Summary
		}
	}
	return string(raw)
}

func renderEvidence(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil && fields != nil {
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var lines []string
		for _, key := range keys {
			lines = append(lines, key+": "+renderEvidence(fields[key]))
		}
		return strings.Join(lines, "\n")
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return string(raw)
	}
	return pretty.String()
}
