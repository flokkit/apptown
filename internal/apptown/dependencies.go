package apptown

import (
	"context"
	"log"

	"github.com/flokkit/apptown/stock"
)

// vendor dependency kinds at boot. A bad file never breaks startup; each
// one is logged with its reason, and the catalog holds it unreadable so
// every use of that kind says why (dependency.SyncStock).
func (a *App) SyncStockDependencies(ctx context.Context) {
	_, skipped, err := a.Dependencies.SyncStock(ctx, stock.FS, "dependencies")
	for _, why := range skipped {
		log.Printf("ERROR: the shipped dependency kind %s; that kind cannot be used until a release fixes it", why)
	}
	if err != nil && len(skipped) == 0 {
		log.Printf("syncing the stock dependency kinds: %v", err)
	}
}
