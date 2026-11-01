package database

import (
	"context"
	"fmt"
)

// The PostgreSQL major versions AppTown runs its own database on, built-in
// or -db-url. A release supports one range: its backups dump with the
// newest major's pg_dump, which dumps any older server in the range, and
// the built-in database a restore loads into is made at the newest, so a
// dump from anywhere in the range restores. Raising OwnPostgresMax is a
// release of its own: the pinned client image and the built-in database's
// image move with it. App databases are not bound by this.
const (
	ownPostgresMin = 16
	OwnPostgresMax = 16
)

// OwnServerSupported refuses a server (by server_version_num) whose major is
// outside the range, naming the range.
func OwnServerSupported(versionNum int) error {
	major := versionNum / 10000
	if major >= ownPostgresMin && major <= OwnPostgresMax {
		return nil
	}
	want := fmt.Sprintf("PostgreSQL %d", ownPostgresMin)
	if ownPostgresMin != OwnPostgresMax {
		want = fmt.Sprintf("PostgreSQL %d to %d", ownPostgresMin, OwnPostgresMax)
	}
	return fmt.Errorf("AppTown needs %s for its own database; this server is %d", want, major)
}

// OwnServer reads the server's version (server_version_num) and refuses one
// outside the range.
func (d *DB) OwnServer(ctx context.Context) (int, error) {
	var num int
	if err := d.SQL().QueryRowContext(ctx, "SHOW server_version_num").Scan(&num); err != nil {
		return 0, fmt.Errorf("reading the database server's version: %w", err)
	}
	return num, OwnServerSupported(num)
}
