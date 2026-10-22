package httpapi

import "github.com/flokkit/apptown/internal/wiregen"

// The data protection routes' responses.
func init() {
	registerWire(
		wiregen.Named{Name: "BackupStoreState", Value: BackupStoreState{}},
		wiregen.Named{Name: "OperationAdmitted", Value: OperationAdmitted{}},
		wiregen.Named{Name: "Operation", Value: Operation{}},
		wiregen.Named{Name: "FinalCopies", Value: FinalCopies{}},
		wiregen.Named{Name: "DeploymentProtection", Value: DeploymentProtection{}},
		wiregen.Named{Name: "OperationDismissed", Value: OperationDismissed{}},
	)
}
