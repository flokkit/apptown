package forge

import (
	"context"
	"strings"

	piharness "flokkit.io/forge/internal/forge/agentharness/pi"
	"flokkit.io/forge/internal/forge/inference"
	"flokkit.io/forge/internal/forge/workflowexec"
)

func inferenceIdentityFor(a *App) inference.Identity {
	return inference.Identity{DB: a.DB, Console: a.Console, Tokens: &a.runTok}
}

// piModelConfiguration renders the catalog's public view in Pi's models.json
// form. It lives here, not in inference, because inference names no harness,
// and not in the harness, because the harness imports no domain owner. The
// composition root is the one place allowed to know both.
type piModelConfiguration struct {
	catalog *inference.Catalog
	baseURL string
}

func (m piModelConfiguration) ModelsJSON(ctx context.Context) ([]byte, error) {
	public, err := m.catalog.PublicProviders(ctx, m.baseURL)
	if err != nil {
		return nil, err
	}
	providers := make([]piharness.Provider, 0, len(public))
	for _, p := range public {
		providers = append(providers, piharness.Provider{Name: p.Name, URL: p.URL, API: p.API, Metadata: p.Metadata, Models: p.Models})
	}
	return piharness.ModelsJSON(providers)
}

func inferenceRuntimeFor(a *App) piModelConfiguration {
	base := a.cfg.inferenceURL
	if base == "" {
		base = "http://host.docker.internal:7779"
	}
	return piModelConfiguration{catalog: a.providers, baseURL: strings.TrimRight(base, "/")}
}
func piHomeFor(a *App) piharness.Preparer {
	home := piharness.NewPreparer(inferenceRuntimeFor(a), a.modelSelection, a.Adoption, &a.docker, a.cfg.agentImage)
	if a.cfg.noLocalDocker {
		home = home.WithLabels(workerAgentLabels(a.life, a.cfg.worker))
	}
	return home
}
func draftAgentFor(a *App) workflowexec.Drafter {
	return workflowexec.NewDrafter(&a.runTok, a.modelSelection, a.inference.Complete)
}
