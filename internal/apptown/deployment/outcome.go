package deployment

import (
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

type Attempt struct {
	ID           ids.ID
	DeploymentID ids.ID
	Generation   int64
}

type deploymentOutcome string

const (
	outcomeUncertain deploymentOutcome = "uncertain"
	Applied          deploymentOutcome = "applied"
	alreadyApplied   deploymentOutcome = "already-applied"
	Superseded       deploymentOutcome = "superseded"
)

type Completion struct {
	Attempt  Attempt
	Observed deploy.Observation
}
