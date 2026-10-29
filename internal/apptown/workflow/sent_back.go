package workflow

import (
	"encoding/json"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// SentBack is the words a rework in a running run's current stage is working
// from, and who gave them: AppTown works it out once, so the CLI and the web
// say the same.
type SentBack struct {
	StepID ids.ID `json:"stepId"`
	Step   string `json:"step"`
	By     string `json:"by"`
	Note   string `json:"note"`
}

// checkReportPrefix begins the words a check's report is handed back to the
// agent in (retry.go, auto-rework).
const checkReportPrefix = "The previous check reported"

// sentBackOf is the rework the run's current stage is in, or nil. The rework
// is the stage's agent step that is running or waiting and carries send-back
// words in its configuration; no such step is no rework, whatever an earlier
// stage was sent back with. Who sent it back is the newest send-back receipt
// for that step with those words (a person's, naming them); else the stage
// approval's last rejection; else a check when the words are a check's
// report, otherwise a person.
func sentBackOf(run *Run, steps []*Step, events []Event) *SentBack {
	if run.Status != RunRunning {
		return nil
	}
	var agent *Step
	for _, s := range steps {
		if s.Stage == run.CurrentStage && s.Type == "agent/pi" && (s.Status == StatusRunning || s.Status == StatusPending) {
			agent = s
			break
		}
	}
	if agent == nil {
		return nil
	}
	var config struct {
		Sendback string `json:"sendback"`
	}
	if json.Unmarshal(agent.Config, &config) != nil || config.Sendback == "" {
		return nil
	}
	out := &SentBack{StepID: agent.ID, Step: agent.Name, Note: config.Sendback}
	var newest Event
	for _, e := range events {
		var receipt struct {
			StepID ids.ID `json:"stepId"`
			By     string `json:"by"`
			Note   string `json:"note"`
		}
		if e.Kind != "step.sendback" || json.Unmarshal(e.Payload, &receipt) != nil || receipt.StepID != agent.ID || receipt.Note != config.Sendback {
			continue
		}
		if newest.ID.IsZero() || e.ID.Compare(newest.ID) > 0 {
			newest = e
			out.By = receipt.By
		}
	}
	if out.By != "" {
		return out
	}
	for _, s := range steps {
		if s.Stage != run.CurrentStage || s.Type != "approval" {
			continue
		}
		var evidence ApprovalEvidence
		if json.Unmarshal(s.Evidence, &evidence) == nil && evidence.LastRejection != nil {
			out.By = evidence.LastRejection.Approver
			return out
		}
	}
	out.By = "a person"
	if strings.HasPrefix(config.Sendback, checkReportPrefix) {
		out.By = "a check"
	}
	return out
}
