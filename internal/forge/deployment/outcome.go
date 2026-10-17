package deployment

import (
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/ids"
)

type Attempt struct {
	ID           ids.ID
	DeploymentID ids.ID
	Generation   int64
}

type Outcome string

const (
	Uncertain      Outcome = "uncertain"
	Applied        Outcome = "applied"
	AlreadyApplied Outcome = "already-applied"
	Superseded     Outcome = "superseded"
)

type Completion struct {
	Attempt  Attempt
	Observed deploy.Observation
}
