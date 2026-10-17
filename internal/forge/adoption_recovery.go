package forge

import "flokkit.io/forge/internal/forge/workcommands"

// RecoverAdoptions is the daemon startup hook for already-shipped uplift targets.
func (a *App) RecoverAdoptions() { upliftSettlementFor(a).Recover(a.life) }
func upliftSettlementFor(a *App) workcommands.UpliftSettlement {
	return workcommands.UpliftSettlement{DB: a.DB, Adoption: a.Adoption, Lifecycle: a.lifecycle}
}
