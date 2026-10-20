package workflowexec

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
)

// Evals: an execution in a throwaway copy of a project, made for an eval,
// gets nothing the instance holds for ordinary work. Its agent and review steps, here and on a worker, are
// given no instance STEP_ value and no registry credential; its model
// comes through AppTown's inference route as every run's does.

// givenInstanceValues is Project's answer for projectID: may an execution
// there be given what the instance holds? A project Project cannot read is
// an error, never yes.
func (a *Executor) givenInstanceValues(ctx context.Context, projectID ids.ID) (bool, error) {
	purpose, err := project.PurposeIn(ctx, a.db, projectID)
	if err != nil {
		return false, err
	}
	return purpose.Integrations(), nil
}

// stepEnv is the instance's step values an execution in projectID gets:
// none for an eval copy.
func (a *Executor) stepEnv(ctx context.Context, projectID ids.ID) ([]string, error) {
	given, err := a.givenInstanceValues(ctx, projectID)
	if err != nil || !given {
		return nil, err
	}
	return a.secrets.StepEnv(ctx), nil
}

// stepRegistry is the registry an agent's private Docker is logged in to:
// none for an eval copy, whose releases AppTown publishes itself.
func (a *Executor) stepRegistry(ctx context.Context, projectID ids.ID) (*integration.Registry, error) {
	given, err := a.givenInstanceValues(ctx, projectID)
	if err != nil || !given {
		return nil, err
	}
	return a.integrations.RegistryFor(ctx, projectID), nil
}
