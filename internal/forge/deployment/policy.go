package deployment

import (
	"fmt"

	"flokkit.io/forge/internal/fault"
)

// DeploymentPolicy names the deployment procedure, not an assurance that
// application updates are backwards compatible or that their data can be restored.
type DeploymentPolicy string

const (
	CompatibleUpdate     DeploymentPolicy = "compatible-update"
	ProtectedMaintenance DeploymentPolicy = "protected-maintenance"
)

// policyNotImplemented is a recognized procedure that cannot be selected yet.
type policyNotImplemented struct{}

func (policyNotImplemented) Error() string {
	return "protected-maintenance is not implemented; use compatible-update"
}
func (policyNotImplemented) FaultKind() fault.Kind { return fault.Unprocessable }
func (policyNotImplemented) FaultCode() string     { return "deployment_policy_not_implemented" }

func (p DeploymentPolicy) available() (DeploymentPolicy, error) {
	switch p {
	case "", CompatibleUpdate:
		return CompatibleUpdate, nil
	case ProtectedMaintenance:
		return "", policyNotImplemented{}
	default:
		return "", &ConfigurationInputError{fmt.Errorf("unknown deploymentPolicy %q; use compatible-update", p)}
	}
}

// UpdatePolicy reports the enforced procedure for a standing placement. Only
// one procedure can execute, so it is derived, not a saved choice. Previews
// and restored copies do not select a standing deployment's update procedure.
func (e *Deployment) UpdatePolicy() DeploymentPolicy {
	if e.Kind == Standing {
		return CompatibleUpdate
	}
	return ""
}
