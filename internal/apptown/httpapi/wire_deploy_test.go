package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/wiregen"
)

// The deployment, target and release routes' responses.
func init() {
	registerWire(
		wiregen.Named{Name: "DeploymentAddress", Value: deployment.AddressReading{}},
		wiregen.Named{Name: "AddressSnapshot", Value: deployment.AddressSnapshot{}},
		wiregen.Named{Name: "AddressObservation", Value: deployment.AddressObservation{}},
		wiregen.Named{Name: "ReleaseDefault", Value: deployment.ReleaseDefault{}},
		wiregen.Named{Name: "DeploymentBackups", Value: DeploymentBackups{}},
		wiregen.Named{Name: "BackupTaken", Value: BackupTaken{}},
		wiregen.Named{Name: "AllBackups", Value: AllBackups{}},
		wiregen.Named{Name: "RestoreResult", Value: dataprotection.RestoreResult{}},
		wiregen.Named{Name: "DeploymentRemoved", Value: DeploymentRemoved{}},
		wiregen.Named{Name: "ReleaseRequested", Value: ReleaseRequested{}},
		wiregen.Named{Name: "DeploymentChecked", Value: DeploymentChecked{}},
		wiregen.Named{Name: "DeploymentAIModels", Value: DeploymentAIModels{}},
		wiregen.Named{Name: "DeploymentResources", Value: deployment.DeploymentResources{}},
		wiregen.Named{Name: "DeploymentDiagnostics", Value: deployment.Diagnostics{}},
		wiregen.Named{Name: "PartLog", Value: deployment.PartLog{}},
		wiregen.Named{Name: "TargetCapacity", Value: deployment.TargetCapacity{}},
		wiregen.Named{Name: "OverrideResult", Value: deployment.OverrideResult{}},
		wiregen.Named{Name: "InfrastructureTargets", Value: InfrastructureTargets{}},
		wiregen.Named{Name: "DiskReadings", Value: DiskReadings{}},
		wiregen.Named{Name: "ReleaseDetail", Value: ReleaseDetail{}},
		wiregen.Named{Name: "ReleaseListed", Value: ReleaseListed{}},
		wiregen.Named{Name: "ReleasePlan", Value: ReleasePlan{}},
		wiregen.Named{Name: "ReleasePreviewStarted", Value: ReleasePreviewStarted{}},
		wiregen.Named{Name: "RetainedList", Value: RetainedList{}},
		wiregen.Named{Name: "RemoteNamespaces", Value: RemoteNamespaces{}},
		wiregen.Named{Name: "RemoteAttached", Value: RemoteAttached{}},
		wiregen.Named{Name: "TargetCheck", Value: TargetCheck{}},
		wiregen.Named{Name: "CloudAccountCheck", Value: CloudAccountCheck{}},
		wiregen.Named{Name: "TargetAttached", Value: TargetAttached{}},
		wiregen.Named{Name: "CloudTargetAttached", Value: CloudTargetAttached{}},
		wiregen.Named{Name: "TargetEdited", Value: TargetEdited{}},
		wiregen.Named{Name: "TargetPage", Value: TargetPage{}},
		wiregen.Named{Name: "TargetChecked", Value: TargetChecked{}},
		wiregen.Named{Name: "TargetRetired", Value: TargetRetired{}},
		wiregen.Named{Name: "TargetList", Value: TargetList{}},
		wiregen.Named{Name: "TargetOffers", Value: TargetOffers{}},
		wiregen.Named{Name: "ProjectTargets", Value: ProjectTargets{}},
		wiregen.Named{Name: "TierMap", Value: TierMap{}},
		wiregen.Named{Name: "Departures", Value: Departures{}},
	)
}
