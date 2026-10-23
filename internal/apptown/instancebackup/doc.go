// Package instancebackup is a whole-AppTown backup and restore
// (stock/docs/guides/apptown-backups.md): one archive, encrypted with a
// passphrase, holding the built-in database's dump, the data directory
// (without pgdata and cache) and a manifest, with a set of each built-in
// deployment's data taken with its app stopped; and its restore onto a
// fresh host.
//
// It composes owners and owns no table: data protection takes the sets,
// keeps the backup-file lock, moves the records of sets kept on disk, records
// the fills and copies an archive to the backup store; deployment lists the
// built-in host's deployments and holds those a restore must not act on;
// secret says whether an external store keeps values; the composition root
// makes and dumps the built-in database (BuiltinDatabase).
//
// The running AppTown takes a backup, asked through its control socket by
// `apptownd backup` (control.go), because only it can stop its
// deployments' parts under its own claims and hold its own new work (Hold).
// A restore runs before the restored AppTown first starts (restore.go).
package instancebackup
