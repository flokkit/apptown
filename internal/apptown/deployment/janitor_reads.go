package deployment

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"
)

type JanitorDeployment struct {
	ProjectID    ids.ID         `json:"projectId"`
	DeploymentID ids.ID         `json:"deploymentId"`
	Name         string         `json:"name"`
	DisplayLabel string         `json:"displayLabel"`
	Setting      RoutineUpdates `json:"setting"`
	Protected    bool           `json:"protected"`
	Automatic    bool           `json:"automatic"`
	Sentence     string         `json:"sentence"`
	Hold         *JanitorHold   `json:"hold,omitempty"`
}

type JanitorChangeRelease struct {
	ChangeID     ids.ID `json:"changeId"`
	ProjectID    ids.ID `json:"projectId"`
	Number       int64  `json:"number"`
	State        string `json:"state"`
	Deployment   string `json:"deployment"`
	DisplayLabel string `json:"displayLabel"`
}

func (s *Service) requireJanitorProjects(ctx context.Context, projects []ids.ID) error {
	if s.authority == nil {
		return access.ErrAuthorityUnavailable
	}
	for _, pid := range projects {
		resource, err := project.AuthorityResourceIn(ctx, s.db, pid)
		if err != nil {
			return err
		}
		if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: resource}); err != nil {
			return err
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
		protected := (&Deployment{Audience: row.Audience, DataClass: row.DataClass}).Protected()
		item := JanitorDeployment{ProjectID: row.ProjectID, DeploymentID: row.ID, Name: row.Name, DisplayLabel: row.DisplayLabel, Setting: setting, Protected: protected, Automatic: !protected && setting.Mode != "person", Sentence: janitorReleaseSentence(setting, protected)}
		if hold, ok := holds[row.ID]; ok {
			item.Hold = &hold
		}
		out = append(out, item)
	}
	return out, nil
}

func janitorReleaseSentence(setting RoutineUpdates, protected bool) string {
	sentence := "Release setting not known."
	switch setting.Mode {
	case "automatic":
		sentence = "Releases accepted updates automatically."
	case "person":
		sentence = "Releases accepted updates when a person says."
	case "window":
		sentence = fmt.Sprintf("Releases accepted updates on %s, %s–%s (%s).", setting.Days, setting.From, setting.To, setting.Zone)
	}
	if protected {
		return "Selected setting: " + sentence + " A person must approve each release to this protected deployment."
	}
	return sentence
}

// JanitorReleaseHoldSentence describes the same release-health rule used by
// HeldBackIn and release admission, without another stored policy.
func JanitorReleaseHoldSentence() string {
	return "Stops a project after a failed release or uncertain release progress, and never releases over a rollback. A person's successful release of the newest code ends the hold."
}

func (s *Service) JanitorChangeReleases(ctx context.Context, projects, changes []ids.ID) ([]JanitorChangeRelease, error) {
	if err := s.requireJanitorProjects(ctx, projects); err != nil {
		return nil, err
	}
	rows, err := deploymentsql.New(s.db.SQL()).JanitorChangeReleases(ctx, deploymentsql.JanitorChangeReleasesParams{ProjectIds: projects, ChangeIds: changes})
	if err != nil {
		return nil, err
	}
	out := make([]JanitorChangeRelease, 0, len(rows))
	for _, r := range rows {
		out = append(out, JanitorChangeRelease{ChangeID: r.ChangeID, ProjectID: r.ProjectID, Number: r.Number, State: r.State, Deployment: r.Deployment, DisplayLabel: r.DisplayLabel})
	}
	return out, nil
}
