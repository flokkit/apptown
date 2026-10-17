package forge

import (
	"context"
	"log"
)

// Workflow library: the instance-level store of workflows, the only
// source of one. Vendor entries sync from the embedded stock/ directory at
// startup (§7.1); a project selects lanes from it and copies nothing
// (design/decisions/workflows-as-lanes.md); a run records what ran as its origin
// ("stock/<name>@<version>", "lib/<name>@<version>") — the receipt.

// SyncStock puts the release's shipped ways of working into the store
// (internal/forge/wow), then lets adopted policies' wording follow the
// library: the rule a project adopted stays pinned forever, but the
// sentence describing it tracks the library, so improved wording appears
// on every surface at once. main and the journey harness call this so
// tests and production use the same stock. Between the workflows and the
// packs, private Docker is declared where skill/implementation gave it
// (adoption.DeclarePrivateDocker), so a pack pins the declared versions.
func (a *App) SyncStock(ctx context.Context) {
	a.Ways.SyncStockWorkflows(ctx)
	if err := a.Adoption.DeclarePrivateDocker(ctx, a.Ways); err != nil {
		log.Printf("declaring private Docker in workflows: %v", err)
	}
	a.Ways.SyncStockGuidance(ctx)
	a.Ways.SyncStockPolicies(ctx)
	a.Ways.SyncStockPacks(ctx)
	if err := a.Adoption.RefreshPolicyWording(ctx); err != nil {
		log.Printf("refreshing adopted policies' wording from the library: %v", err)
	}
}
