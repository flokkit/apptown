package deployment

import (
	"context"
	"encoding/json"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/stamp"

	"github.com/flokkit/apptown/internal/ids"
)

// LandingDeployStep is the recorded execution evidence supplied by Work. This
// read issues no workflow claim and grants no recovery operation.
type LandingDeployStep struct {
	ID         ids.ID          `json:"id"`
	RunID      ids.ID          `json:"runId"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	Config     json.RawMessage `json:"config"`
	Evidence   json.RawMessage `json:"evidence"`
	StartedAt  stamp.Time      `json:"startedAt"`
	FinishedAt stamp.Time      `json:"finishedAt"`
}

type LandingDeployAttempt struct {
	OutcomeKnown bool              `json:"outcomeKnown"`
	Step         LandingDeployStep `json:"step"`
	Attempt      int64             `json:"attempt"`
	ChangeID     ids.ID            `json:"changeId"`
	ChangeNumber int64             `json:"changeNumber"`
	ChangeTitle  string            `json:"changeTitle"`
	RunStartedAt string            `json:"runStartedAt"`
}

type landingDeployReader interface {
	LandingDeployAttempts(context.Context, ids.ID) ([]LandingDeployAttempt, error)
}

// DeployHistory is the landing invocations for a named deployment. Releases are
// still created by successful landings, never reserved by this read.
func (s *Service) DeployHistory(ctx context.Context, project ids.ID, name string) ([]LandingDeployAttempt, error) {
	records, err := s.authorizedListing(ctx, project)
	if err != nil {
		return nil, err
	}
	found := false
	for _, r := range records {
		if r.summary.Name == name {
			found = true
			break
		}
	}
	out := make([]LandingDeployAttempt, 0)
	if s.landings == nil {
		if !found {
			return nil, fault.New(fault.NotFound, "deployment not found")
		}
		return out, nil
	}
	attempts, err := s.landings.LandingDeployAttempts(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, a := range attempts {
		var config struct {
			Deployment string `json:"deployment"`
		}
		if err := json.Unmarshal(a.Step.Config, &config); err != nil {
			return nil, err
		}
		if config.Deployment == "" {
			config.Deployment = "main"
		}
		if config.Deployment == name {
			out = append(out, a)
		}
	}
	if !found && len(out) == 0 {
		return nil, fault.New(fault.NotFound, "deployment not found")
	}
	return out, nil
}
