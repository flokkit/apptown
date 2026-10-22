package dependency

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
)

type LocalRuntime struct {
	Catalog *Catalog
	// Steps says the services are for AppTown's own steps (a tests step,
	// an agent step), where the app's own tests run: each takes its kind's
	// StepEnv. Never for a deployment or a preview, which run the app as
	// it will run in production.
	Steps bool
	// Credentials are the owner's generated credentials: a deployment's or
	// a preview's (or one rehearsal's). Nil gives the catalogue's own
	// values, as AppTown's own steps use.
	Credentials Credentials
}

// Fulfil is the local services and the variables that reach them for a
// contract's needs. It refuses, before anything starts, the needs the
// catalogue refuses as declared (RefusesNeeds): a refusal is a *Refused
// with no services, a failed read the error. Needs the catalogue does not
// hold are an *UnknownNeeds returned with the services of the needs it
// does hold, and their variables: a caller that goes on without the
// unknown ones (a tests step) keeps the rest.
//
// Only a kind the catalogue answers it does not hold is unknown: one it
// holds unreadable is refused, and a read that fails is the error, never a
// need to go on without.
func (n LocalRuntime) Fulfil(ctx context.Context, needs []string, params map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	var known, unknown []string
	for _, need := range needs {
		_, err := n.Catalog.ByCapability(ctx, need)
		switch {
		case errors.Is(err, ErrUnknown):
			unknown = append(unknown, need)
		case err == nil || errors.Is(err, ErrSpec):
			known = append(known, need) // RefusesNeeds refuses an unreadable one
		default:
			return nil, nil, fmt.Errorf("reading the dependency kind %s: %w", need, err)
		}
	}
	why, err := n.Catalog.RefusesNeeds(ctx, known, params)
	if err != nil {
		return nil, nil, err
	}
	if why != "" {
		return nil, nil, &Refused{Why: why}
	}
	plan, err := n.Catalog.resolveLocal(ctx, known, params, "", n.Credentials)
	if err != nil {
		return nil, nil, err
	}
	if len(plan.Unknown) > 0 {
		// Read a moment ago: whatever this is, it is not a need to go on
		// without.
		return nil, nil, fmt.Errorf("the dependency kinds %s could not be read", strings.Join(plan.Unknown, ", "))
	}
	var services []deploy.ServiceSpec
	for _, s := range plan.Services {
		if n.Steps {
			s = s.ForSteps()
		}
		services = append(services, s.Spec())
	}
	if len(unknown) > 0 {
		return services, plan.Env, &UnknownNeeds{Needs: unknown}
	}
	return services, plan.Env, nil
}

// Data is where the kind's emulator keeps its data, "" for one that keeps
// none or a kind the catalogue does not hold (Fulfil already refused it).
// A kind that cannot be read is an error.
func (n LocalRuntime) Data(ctx context.Context, capability string) (string, error) {
	d, err := n.Catalog.ByCapability(ctx, capability)
	switch {
	case err == nil:
		return d.Local.Data, nil
	case errors.Is(err, ErrUnknown) || errors.Is(err, ErrSpec):
		return "", nil
	}
	return "", fmt.Errorf("reading the dependency kind %s: %w", capability, err)
}
