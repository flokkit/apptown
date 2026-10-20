package integration

import (
	"context"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"slices"
	"strings"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/project"
)

// Evals: a throwaway copy of a project,
// made for an eval, is given no integration (Project's purpose for it
// allows none). Nothing is attached to it,
// provisioned for it or minted for it: an attachment, a provider-side
// project or token, an error-tracker or feedback registration, an SCM
// token. The one thing a copy's releases need, a registry to publish to,
// AppTown supplies itself (evalRegistry): the one offered registry
// connector, under the copy's own repository, with the connector's
// credential, which AppTown uses and never hands to the copy.

// ErrNoIntegrations refuses an integration effect for a project whose
// purpose allows none: an eval copy.
var ErrNoIntegrations = fault.New(fault.Unprocessable, "this project is an eval copy, which is given no integrations")

// refusesIntegrations is asked before any integration effect for
// projectID: Project's purpose for it, read through q. A project Project
// cannot read refuses.
func refusesIntegrations(ctx context.Context, q database.Queryer, projectID ids.ID) error {
	purpose, err := project.PurposeIn(ctx, q, projectID)
	if err != nil {
		return err
	}
	if !purpose.Integrations() {
		return ErrNoIntegrations
	}
	return nil
}

// EvalPublication is the repository an eval copy's releases are published
// to, and the credential that writes and reads it there: for AppTown's
// own publication and the pull of what it published, never for an image
// the copy's code names. Not a copy: an error.
func (s *Service) EvalPublication(ctx context.Context, projectID ids.ID) (*Registry, error) {
	// Publication through AppTown's own registry is an eval copy's act,
	// not the absence of integrations: another purpose would be judged here.
	if purpose, err := s.projects.Purpose(ctx, projectID); err != nil {
		return nil, err
	} else if purpose != project.EvalCopy {
		return nil, fmt.Errorf("project %d is not an eval copy", projectID)
	}
	return s.evalRegistry(ctx, projectID)
}

// evalRegistry is where an eval copy's releases are published: the one
// container-registry connector AppTown offers, at <url>/<group>/<slug>
// for the copy's own slug, with the connector's credential. A GitLab
// connector would provision a project and a token for the copy, which a
// copy is never given; with none, several or a GitLab one, the error says
// so.
func (s *Service) evalRegistry(ctx context.Context, projectID ids.ID) (*Registry, error) {
	p, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	connectors, err := ForCapability(ctx, s.Bindings.db, CapContainerRegistry)
	if err != nil {
		return nil, err
	}
	if len(connectors) != 1 {
		return nil, fmt.Errorf("an eval copy publishes its releases to the one container-registry connector AppTown offers, and it offers %d", len(connectors))
	}
	connector := connectors[0]
	cfg := parseJSONMap(connector.ConfigJSON)
	if provider, _ := cfg["provider"].(string); provider == "gitlab" {
		return nil, fmt.Errorf("the container-registry connector %s provisions a GitLab project per project, which an eval copy is never given; offer a registry connector with its own credential for evals", connector.Name)
	}
	host, _ := cfg["url"].(string)
	group, _ := cfg["group"].(string)
	if host == "" {
		return nil, fmt.Errorf("the container-registry connector %s names no url", connector.Name)
	}
	info := &Registry{Host: host, Repo: strings.Join(slices.DeleteFunc([]string{strings.TrimSuffix(host, "/"), strings.Trim(group, "/"), p.Slug}, func(s string) bool { return s == "" }), "/")}
	if from := ConfigStrings(cfg["tokenFrom"]); len(from) > 0 {
		info.Username, _ = cfg["username"].(string)
		if s.secrets != nil {
			info.Token = registryToken(s.secrets.All(ctx), from)
		}
		if info.Token == "" {
			return info, fmt.Errorf("the container-registry connector %s names its credential %s, which the secrets store does not hold", connector.Name, strings.Join(from, ", "))
		}
	}
	return info, nil
}
