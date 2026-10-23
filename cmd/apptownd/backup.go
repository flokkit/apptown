package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/flokkit/apptown/internal/apptown"
	"github.com/flokkit/apptown/internal/apptown/instancebackup"
	"github.com/flokkit/apptown/internal/backuparchive"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// The two commands of a whole-AppTown backup (stock/docs/guides/apptown-backups.md):
//
//	apptownd [flags] backup [-store] <archive>
//	apptownd [flags] restore <archive>
//
// Both take the daemon's own flags and environment (-data names the data
// directory). Backup asks the AppTown running on that data directory, over
// its control socket, to write the archive; restore unpacks an archive on a
// host with no AppTown, before AppTown first starts there.

// passphraseFileEnv names a file holding the archive's passphrase, for use
// without a terminal.
const passphraseFileEnv = "APPTOWN_BACKUP_PASSPHRASE_FILE"

const commandsUsage = `
Commands, after the flags:
  backup [-store] <archive>  ask the AppTown running on -data to write a
                             whole-AppTown archive at <archive> (outside the
                             data directory); -store also copies it to the
                             backup store
  restore <archive>          restore an archive into an empty -data on a host
                             with no AppTown, then start AppTown normally
The archive's passphrase is asked on the terminal, or read from the file
` + passphraseFileEnv + ` names. Without it the archive cannot be opened.
`

// command is a subcommand and its arguments, or none.
type command struct {
	name    string
	archive string
	store   bool
}

// parseCommand reads the arguments after the daemon's flags.
func parseCommand(args []string) (command, error) {
	if len(args) == 0 {
		return command{}, nil
	}
	c := command{name: args[0]}
	switch c.name {
	case "backup", "restore":
	default:
		return c, fmt.Errorf("unexpected argument %q", args[0])
	}
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if c.name == "backup" {
		fs.BoolVar(&c.store, "store", false, "also copy the archive to the backup store")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return c, fmt.Errorf("%s: %w", c.name, err)
	}
	if fs.NArg() != 1 {
		return c, fmt.Errorf("%s takes one archive: apptownd [flags] %s <archive>", c.name, c.name)
	}
	abs, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return c, err
	}
	c.archive = abs
	return c, nil
}

// readPassphrase reads the archive's passphrase from the file
// APPTOWN_BACKUP_PASSPHRASE_FILE names, or from the terminal, twice for a
// new archive. Its last line break is not part of it.
func readPassphrase(confirm bool) (string, error) {
	if path := os.Getenv(passphraseFileEnv); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading the passphrase: %w", err)
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no terminal to ask the passphrase on: put it in a file and name the file in %s", passphraseFileEnv)
	}
	defer tty.Close()
	ask := func(prompt string) (string, error) {
		fmt.Fprint(tty, prompt)
		b, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		return string(b), err
	}
	p, err := ask("the archive's passphrase: ")
	if err != nil || !confirm {
		return p, err
	}
	again, err := ask("the same passphrase again: ")
	if err != nil {
		return "", err
	}
	if again != p {
		return "", errors.New("the two passphrases differ")
	}
	return p, nil
}

// runBackup asks the running AppTown for a backup and prints its report.
func runBackup(ctx context.Context, dataDir string, c command) error {
	pass, err := readPassphrase(true)
	if err != nil {
		return err
	}
	if len(pass) < backuparchive.MinPassphrase {
		return fmt.Errorf("the passphrase must be at least %d characters", backuparchive.MinPassphrase)
	}
	who := ""
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	report, err := instancebackup.AskControl(ctx, dataDir, instancebackup.ControlRequest{Op: "backup", Archive: c.archive, Passphrase: pass, Store: c.store, User: who},
		func(s string) { fmt.Fprintln(os.Stderr, "==", s) })
	if err != nil {
		return err
	}
	m := report.Manifest
	fmt.Printf("archive: %s (%d bytes, sha256 %s)\n", report.Archive, report.Bytes, report.SHA256)
	fmt.Printf("AppTown %s, %s, schema %d; new work was held for %s\n", m.AppTown, m.Build, m.Schema, report.Held.Round(1e9))
	for _, d := range m.Deployments {
		switch {
		case d.NoData:
			fmt.Printf("  %s/%s: holds no data\n", d.Project, d.Deployment)
		case d.Complete():
			fmt.Printf("  %s/%s: set %s, taken with its app stopped\n", d.Project, d.Deployment, d.Set)
		default:
			fmt.Printf("  %s/%s: NOT WHOLE: %s\n", d.Project, d.Deployment, d.Why)
		}
	}
	if m.ExternalSecrets != "" {
		fmt.Println("external secrets:", m.ExternalSecrets)
	}
	if len(m.Left) > 0 {
		fmt.Println("left out of the data directory (sockets and the like):", strings.Join(m.Left, ", "))
	}
	if report.StoreKey != "" {
		fmt.Println("in the backup store as", report.StoreKey)
	}
	fmt.Println("Keep the passphrase apart from the archive: without it the archive cannot be restored.")
	if len(report.Problems) > 0 {
		return fmt.Errorf("the backup is incomplete:\n  %s", strings.Join(report.Problems, "\n  "))
	}
	return nil
}

// restoreStep unpacks and loads the archive; the caller then opens the
// database and builds AppTown as a verify-only start does, and finishes
// with finishRestore.
func restoreStep(ctx context.Context, dataDir string, c command) (*instancebackup.Manifest, error) {
	pass, err := readPassphrase(false)
	if err != nil {
		return nil, err
	}
	dk := docker.Scope{Client: docker.CLI{}}
	return instancebackup.Restore(ctx, instancebackup.RestoreRequest{DataDir: dataDir, Archive: c.archive, Passphrase: pass, Docker: dk,
		Database: apptown.BuiltinDatabase{DataDir: dataDir, Docker: dk}, Say: func(s string) { fmt.Fprintln(os.Stderr, "==", s) }})
}

// finishRestore records what the restored AppTown does on its first start
// and says what comes next.
func finishRestore(ctx context.Context, app *apptown.App, m *instancebackup.Manifest) error {
	r, err := app.Backups.AfterRestore(ctx, m)
	if err != nil {
		return err
	}
	fmt.Printf("restored AppTown %s, as backed up %s on %s\n", m.AppTown, m.TakenAt, m.Host)
	if r.Relocated > 0 {
		fmt.Printf("the records of %d sets kept on disk now point at this data directory\n", r.Relocated)
	}
	for _, f := range r.Filling {
		fmt.Printf("  %s: rebuilt on the first start, its data put back before its app starts\n", f)
	}
	for _, f := range r.NotFilled {
		fmt.Printf("  %s\n", f)
	}
	for _, h := range r.Held {
		fmt.Printf("  %s: checked against its provider before anything acts on it\n", h)
	}
	if m.ExternalSecrets != "" {
		fmt.Println("external secrets:", m.ExternalSecrets)
	}
	fmt.Println("Start AppTown normally, with the same flags (or once with -verify-only first, to look).")
	fmt.Println("This AppTown has the same id as the one backed up: run only one of them.")
	return nil
}
