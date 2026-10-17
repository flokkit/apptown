package forge

import (
	"context"
	"log"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/dbschema"
	"flokkit.io/forge/internal/forge/jobs"
)

// OpenURL connects, migrates, and seeds what every database carries.
func OpenURL(ctx context.Context, url string) (*database.DB, error) {
	db, err := dbschema.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := jobs.Migrate(ctx, db.Pool()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// readOK is lookup for a read whose absence is a legitimate outcome (a
// default applies) but whose failure is not, in code with nobody to hand
// the failure to: no row is quiet, a failure is logged with its statement.
// Reports whether a row was read.
func readOK(err error) bool {
	found, err := database.Lookup(err)
	if err != nil {
		log.Printf("read failed: %v", err)
	}
	return found
}
