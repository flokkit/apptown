package deployment

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// Evals: an eval re-runs finished work
// in throwaway copies of projects, which deploy and preview like any
// project but only on the eval domain. One fact decides everything here,
// Project's purpose for the project: is it served on the eval domain only? Everything an
// eval creates is served on the eval domain and nowhere else, and nothing
// else is served there.
//
//   - The built-in host serves copies at the eval tier domain
//     (-tier-domain eval=...), and every other project at its ordinary
//     domains, as before. A copy is refused on it when no eval domain is
//     configured.
//   - An attached Docker host may be marked for evals: its ingress domain
//     is then the eval domain, and it takes copies and nothing else.
//   - Provider targets are never eval targets: a copy is refused there.
//
// The refusal (refusesPlacement) is asked before anything is provisioned
// at each door that places a deployment on a target: a landing, a deploy
// request and its execution, a binding, and a standing rebuild.

// ingressEvals is the ingress key marking an attached Docker host for
// evals: its domain is the eval domain, and it takes eval copies only.
const ingressEvals = "evals"

// forEvals says whether the target is an attached host marked for evals.
func (t *Target) forEvals() bool {
	marked, _ := t.Ingress[ingressEvals].(bool)
	return marked && t.Origin == OriginAttached && t.Kind == KindDockerHost
}

// refusesPlacement is the one placement rule for evals: a copy only on an
// eval target, anything else never on one. It is refused, with the next
// action, before anything is provisioned.
func (s *Service) refusesPlacement(ctx context.Context, projectID ids.ID, t *Target) error {
	purpose, err := s.env.Purpose(ctx, projectID)
	if err != nil {
		return err
	}
	copy := purpose.EvalDomainOnly()
	switch {
	case copy && t.Origin == OriginBuiltIn && t.Kind == KindDockerHost:
		if s.env.routes.EvalDomain == "" {
			return refused("this project is an eval copy, served only on the eval domain, and this AppTown has none: start it with -tier-domain eval=<domain>, or attach a Docker host for evals")
		}
		return nil
	case copy && !t.forEvals():
		return refused("this project is an eval copy, and %s is not an eval target: copies deploy only to this AppTown's own host on the eval domain, or to a Docker host attached for evals", t.Name)
	case !copy && t.reserved() != "":
		return refused("%s is kept for %s and takes nothing else; deploy this project to another target", t.Name, t.reserved())
	}
	return nil
}

// evalLandingTarget is the target an eval copy's landing creates its
// missing deployment on: the one Docker host attached for evals, else this
// AppTown's own host when it has local Docker and an eval domain. With none,
// or several attached for evals, the refusal names the choice.
func (s *Service) evalLandingTarget(ctx context.Context, name string) (*Target, error) {
	targets, err := s.Targets.All(ctx)
	if err != nil {
		return nil, err
	}
	var eval []*Target
	var builtIn *Target
	for _, t := range targets {
		switch {
		case t.forEvals() && t.Status.Offerable():
			eval = append(eval, t)
		case t.Origin == OriginBuiltIn && t.Kind == KindDockerHost && t.Name == s.hosts.settings.BuiltInTarget:
			builtIn = t
		}
	}
	switch {
	case len(eval) == 1:
		return eval[0], nil
	case len(eval) > 1:
		names := make([]string, len(eval))
		for i, t := range eval {
			names[i] = t.Name
		}
		return nil, refused("deployment %s of an eval copy does not exist and several Docker hosts are attached for evals (%s); keep one for evals", name, strings.Join(names, ", "))
	case builtIn != nil && !s.hosts.settings.NoLocalDocker && s.env.routes.EvalDomain != "":
		return builtIn, nil
	}
	return nil, refused("deployment %s of an eval copy has nowhere to go: start this AppTown with -tier-domain eval=<domain>, or attach a Docker host for evals", name)
}

// forProject is the routing a project's addresses use: for an eval copy,
// the eval domain stands for both the preview and the main domain.
func (r Routing) forProject(copy bool) Routing {
	if copy {
		r.PreviewScheme, r.PreviewDomain = r.EvalScheme, r.EvalDomain
		r.MainScheme, r.MainDomain = r.EvalScheme, r.EvalDomain
	}
	return r
}
