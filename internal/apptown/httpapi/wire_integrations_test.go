package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/wiregen"
)

// The integration, secret and configuration routes' responses.
func init() {
	registerWire(
		wiregen.Named{Name: "ContractUnparsed", Value: ContractUnparsed{}},
		wiregen.Named{Name: "ContractCheck", Value: ContractCheck{}},
		wiregen.Named{Name: "DevComposition", Value: DevComposition{}},
		wiregen.Named{Name: "ConnectorCatalog", Value: ConnectorCatalog{}},
		wiregen.Named{Name: "IntegrationSaved", Value: IntegrationSaved{}},
		wiregen.Named{Name: "DependencyList", Value: DependencyList{}},
		wiregen.Named{Name: "CapabilitySaved", Value: CapabilitySaved{}},
		wiregen.Named{Name: "IntegrationDocs", Value: IntegrationDocs{}},
		wiregen.Named{Name: "IntegrationList", Value: IntegrationList{}},
		wiregen.Named{Name: "AttachmentList", Value: AttachmentList{}},
		wiregen.Named{Name: "AttachableList", Value: AttachableList{}},
		wiregen.Named{Name: "AttachmentEntered", Value: AttachmentEntered{}},
		wiregen.Named{Name: "IntegrationWiring", Value: IntegrationWiring{}},
		wiregen.Named{Name: "ProjectServiceList", Value: ProjectServiceList{}},
		wiregen.Named{Name: "ServicesSeeded", Value: ServicesSeeded{}},
		wiregen.Named{Name: "ServicesProposed", Value: ServicesProposed{}},
		wiregen.Named{Name: "SecretNameList", Value: SecretNameList{}},
		wiregen.Named{Name: "InstanceSecret", Value: InstanceSecret{}},
		wiregen.Named{Name: "SecretStoreList", Value: SecretStoreList{}},
		wiregen.Named{Name: "ProjectSecrets", Value: ProjectSecrets{}},
		wiregen.Named{Name: "SecretProfileSet", Value: SecretProfileSet{}},
		wiregen.Named{Name: "SecretBound", Value: SecretBound{}},
		wiregen.Named{Name: "SecretRevealed", Value: SecretRevealed{}},
		wiregen.Named{Name: "ServiceKindList", Value: ServiceKindList{}},
		// Types they reach, named for this domain so no other group's
		// Kind or Health can take the name.
		wiregen.Named{Name: "DependencyKind", Value: dependency.Kind{}},
		wiregen.Named{Name: "ServiceKindDefinition", Value: integration.Definition{}},
		wiregen.Named{Name: "IntegrationHealth", Value: integration.Health{}},
		wiregen.Named{Name: "IntegrationField", Value: integration.Field{}},
		wiregen.Named{Name: "SecretStoreInfo", Value: secret.StoreInfo{}},
	)
}
