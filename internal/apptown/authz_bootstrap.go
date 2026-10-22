package apptown

import "context"

// Authorization bootstrap and explicit grant operations. Existing authority
// is read from grants; startup never reconstructs it from retired permissions.

// EnsureAuthorization is required startup state. A failed default or admin
// setup aborts the transaction and is returned to the server's startup gate.
func (a *App) EnsureAuthorization(ctx context.Context) error {
	return a.Access.EnsureDefaults(ctx, a.cfg.authMode == "" || a.cfg.authMode == "open")
}
