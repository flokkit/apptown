// Package instancebackup is a whole-AppTown backup and restore
// (stock/docs/guides/apptown-backups.md): one archive, encrypted with a
// passphrase, holding the built-in database's dump, the data directory
// (without pgdata and cache, with the deployment backups kept on disk) and
// a manifest; and its restore onto a fresh host, where each built-in
// deployment is filled from its own newest whole set.
//
// It composes owners and owns no table: data protection names each
// deployment's newest whole set, keeps the backup-file lock, moves the records of sets kept on disk, records
// the fills and copies an archive to the backup store; deployment lists the
// built-in host's deployments and holds those a restore must not act on;
// secret says whether an external store keeps values; the composition root
// makes the built-in database and dumps the database AppTown runs on, in a
// pg_dump container of the server's major (BuiltinDatabase, databaseDump).
//
// The running AppTown takes a backup, asked through its control socket by
// `apptownd backup` (control.go), because only it can hold its own new work
// (Hold). No app is stopped for it.
// A restore runs before the restored AppTown first starts (restore.go).
package instancebackup
