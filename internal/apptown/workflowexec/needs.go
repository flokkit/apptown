package workflowexec

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
)

// needStarted contains only names; connection values must never enter evidence.
type needStarted struct {
	Kind string   `json:"kind"`
	Env  []string `json:"env"`
}

type needsEvidence struct {
	Started []needStarted `json:"started,omitempty"`
	Refused string        `json:"refused,omitempty"`
}

func (a *Executor) describeNeeds(ctx context.Context, services []deploy.ServiceSpec) ([]needStarted, error) {
	var out []needStarted
	for _, service := range services {
		kind, err := a.dependencies.ByCapability(ctx, service.Name)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(kind.Local.Inject))
		for name := range kind.Local.Inject {
			names = append(names, name)
		}
		slices.Sort(names)
		out = append(out, needStarted{Kind: service.Name, Env: names})
	}
	return out, nil
}

func (n needsEvidence) prompt() string {
	var b strings.Builder
	if len(n.Started) > 0 {
		b.WriteString("\n\nAppTown started these declared needs for this step (connection environment variable names only):\n")
		for _, need := range n.Started {
			fmt.Fprintf(&b, "- %s: %s\n", need.Kind, strings.Join(need.Env, ", "))
		}
		b.WriteString("Use these services through the injected environment for integration and end-to-end tests.")
	}
	if n.Refused != "" {
		b.WriteString("\n\nAppTown refuses this app's declared needs in .apptown/app.yaml, so their services were not started for you: " + n.Refused + ".\nAppTown cannot test, preview or release the app with these needs. If the app declares a need incorrectly (including parse errors or bad parameters), fix .apptown/app.yaml as part of this change and say so in your report.\nOnly when a correctly declared kind is missing from the catalogue, file `apptown complain`: that is a platform failure. Report which tests could not run.")
	}
	return b.String()
}
