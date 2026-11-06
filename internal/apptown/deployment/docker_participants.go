package deployment

import "github.com/flokkit/apptown/internal/ids"

// ClaimedExecution is Work's admitted execution, never transport input. The
// participant checks its kind and uses its identity; Work owns claim/finish and
// their transaction guards. Implementations belong to trusted composition.
type ClaimedExecution interface {
	ClaimedStep(kind string) (projectID, runID, stepID ids.ID, err error)
}
