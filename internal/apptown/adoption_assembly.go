package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/strs"
)

// waysImporters lets a stock release say how many projects stay behind a
// block it moved: adoption's fact, read after the fact for the boot log.
type waysImporters struct{ db *database.DB }

func (w waysImporters) Behind(ctx context.Context, blockID ids.ID, version int) (int, error) {
	return adoption.ImportersBehindIn(ctx, w.db, blockID, version)
}

// waysChecks reads a shipped block's checks as conformance does: valid, and
// re-serialised through the Go marshaller so the stored and compared form
// is canonical (the file's JSON is hand-formatted; otherwise every
// check-carrying block would spuriously bump on the first sync after a release).
type waysChecks struct{}

func (waysChecks) Canonical(checksJSON string) (string, error) {
	cs, err := check.ParseDeclarations(checksJSON)
	if err != nil {
		return "", err
	}
	return strs.JSON(cs)
}

type adoptionCapabilities struct{}

func (adoptionCapabilities) HasCapabilitiesIn(ctx context.Context, q database.Queryer, projectID ids.ID, required []string) error {
	return integration.HasCapabilitiesIn(ctx, q, projectID, required)
}
