package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/wiregen"
)

func init() {
	registerWireMapped(dataprotection.Backup{}, "Backup")
	registerWireMapped(dataprotection.ProjectBackup{}, "ProjectBackup")
	registerWire(wiregen.Named{Name: "Backup", Value: StatusBackup{}}, wiregen.Named{Name: "ProjectBackup", Value: statusProjectBackup{}})
	registerWireMapped(remediation.Remediation{}, "Remediation")
	registerWireMapped(remediation.Item{}, "Item")
	registerWire(wiregen.Named{Name: "Remediation", Value: statusRemediation{}}, wiregen.Named{Name: "Item", Value: statusItem{}})
	registerWire(wiregen.Named{Name: "Step", Value: statusStep{}},
		wiregen.Named{Name: "Coverage", Value: statusCoverage{}},
		wiregen.Named{Name: "WhatIf", Value: statusWhatIf{}},
		wiregen.Named{Name: "WhatIfProject", Value: statusWhatIfProject{}},
		wiregen.Named{Name: "WhatIfRow", Value: statusWhatIfRow{}},
	)

	registerWireMapped(workflow.Step{}, "Step")
	registerWireMapped(operations.Operation{}, "Operation")
	registerWireMapped(dataprotection.Coverage{}, "Coverage")
	registerWireMapped(conformance.WhatIf{}, "WhatIf")
	registerWireMapped(conformance.WhatIfProject{}, "WhatIfProject")
	registerWireMapped(conformance.WhatIfRow{}, "WhatIfRow")
}

type statusStep workflow.Step
type statusCoverage dataprotection.Coverage
type statusWhatIf conformance.WhatIf
type statusWhatIfProject conformance.WhatIfProject
type statusWhatIfRow conformance.WhatIfRow

type statusRemediation remediation.Remediation
type statusItem remediation.Item

type StatusBackup dataprotection.Backup
type statusProjectBackup struct {
	StatusBackup
	Project string `json:"project"`
}
