package attention

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
)

// Integrations consumes only retained owner checks. It never calls a provider.
type Integrations struct {
	Deployments interface {
		StandingByProvider(context.Context) (map[string][]deployment.ProviderDeployment, error)
	}
	Service interface {
		RecordedHealth(context.Context, string) (integration.Health, error)
	}
	Bindings interface {
		All(context.Context) ([]integration.Binding, error)
	}
	Attachments interface {
		ProjectsByBinding(context.Context) (map[string][]integration.AttachedProject, error)
	}
}

func (s Integrations) Snapshot(ctx context.Context) (Snapshot, error) {
	bindings, err := s.Bindings.All(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	projects, err := s.Attachments.ProjectsByBinding(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	deployments, err := s.Deployments.StandingByProvider(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Known: true}
	for _, b := range bindings {
		h, err := s.Service.RecordedHealth(ctx, b.Name)
		if err != nil {
			return Snapshot{}, err
		}
		label := b.DisplayLabel
		if label == "" {
			label = b.Name
		}
		at, _ := time.Parse(time.RFC3339, h.CheckedAt)
		href := "/integrations/" + url.PathEscape(b.Name)
		if !h.Automatic {
			continue
		}
		out.Coverage = append(out.Coverage, Coverage{Key: b.Name, Name: label, Href: href, ObservedAt: at, Stale: h.Stale, NextStep: "An administrator can test the connection or correct its settings."})
		if h.LastResult != integration.HealthFailed && h.StateLabel != integration.HealthFailed {
			continue
		}
		affected := []string{}
		for _, p := range projects[b.Name] {
			affected = append(affected, p.Slug)
		}
		for _, d := range deployments[b.Name] {
			affected = append(affected, d.Project)
		}
		detail := "The connection check failed. Ask an administrator to check its settings and test the connection."
		if len(affected) > 0 {
			detail += " Affected apps: " + strings.Join(affected, ", ") + "."
		}
		out.Items = append(out.Items, Item{Key: b.Name, Title: label, Detail: detail, Href: href, Severity: SeverityWarning, ObservedAt: at, Stale: h.Stale})
	}
	return out, nil
}
