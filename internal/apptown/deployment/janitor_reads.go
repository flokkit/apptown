package deployment

import (
	"context"
	"encoding/json"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type JanitorDeployment struct {
	ProjectID    ids.ID       `json:"projectId"`
	DeploymentID ids.ID       `json:"deploymentId"`
	Name         string       `json:"name"`
	DisplayLabel string       `json:"displayLabel"`
	Automatic    bool         `json:"automatic"`
	Hold         *JanitorHold `json:"hold,omitempty"`
}

func (s *Service) requireJanitorProjects(ctx context.Context, projects []ids.ID) error {
	authority, ok := s.authority.(access.ReadAuthorizer)
	if !ok {
		return access.ErrAuthorityUnavailable
	}
	visible, err := authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return err
	}
	allowed := map[ids.ID]bool{}
	for _, id := range visible {
		allowed[id] = true
	}
	for _, id := range projects {
		if !allowed[id] {
			return fault.New(fault.Forbidden, "project is not readable")
		}
	}
	return nil
}

// JanitorDeployments reads settings and the existing release-health rule across
// the authorized projects. HeldBackIn remains the only hold classifier.
func (s *Service) JanitorDeployments(ctx context.Context, projects []ids.ID) ([]JanitorDeployment, error) {
	if err := s.requireJanitorProjects(ctx, projects); err != nil {
		return nil, err
	}
	rows, err := deploymentsql.New(s.db.SQL()).JanitorDeployments(ctx, projects)
	if err != nil {
		return nil, err
	}
	holds := map[ids.ID]JanitorHold{}
	for _, pid := range projects {
		items, err := s.HeldBack(ctx, pid)
		if err != nil {
			return nil, err
		}
		for _, h := range items {
			holds[h.DeploymentID] = h
		}
	}
	out := make([]JanitorDeployment, 0, len(rows))
	for _, row := range rows {
		var setting RoutineUpdates
		if err := json.Unmarshal(row.RoutineUpdates, &setting); err != nil {
			return nil, err
		}
		protected := row.Protected
		item := JanitorDeployment{ProjectID: row.ProjectID, DeploymentID: row.ID, Name: row.Name, DisplayLabel: row.DisplayLabel, Automatic: !protected && setting.Mode != "person"}
		if hold, ok := holds[row.ID]; ok {
			item.Hold = &hold
		}
		out = append(out, item)
	}
	return out, nil
}
