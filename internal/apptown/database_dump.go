package apptown

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/internal/trust"
)

// databaseDump dumps AppTown's database the one way it is dumped, for the
// daily backup of the built-in database and for a whole-AppTown backup
// (instancebackup.Database): pg_dump --no-owner --no-privileges, plain SQL,
// gzipped, run as a task of the pinned postgres image of the newest major
// AppTown supports for its own database (database.OwnPostgresMax), where
// AppTown runs its containers (dumpPlace). The password reaches pg_dump in
// a generated password file, never in an argument or the environment. The
// built-in database (URL empty) is reached inside its own container's
// network; a -db-url database (URL) over the backend's default network.
type databaseDump struct {
	DB    *database.DB
	Place *dumpPlace
	Tools toolversions.Source
	// DataDir holds db.env, which names the built-in database.
	DataDir string
	// URL is the -db-url database; empty dumps the built-in one db.env
	// names under DataDir.
	URL string
}

// dumpPlace is where the dump's task runs: AppTown's own Docker when it has
// one, otherwise its worker (the composition root chooses, once).
type dumpPlace struct {
	Tasks execution.TaskDriver
	// Results reads a worker task's result files. Nil: the task runs on
	// AppTown's own Docker, and its state folder is made under Scratch and
	// mounted.
	Results interface {
		Results(context.Context, execution.TaskRef) (map[string]execution.ResultFile, error)
	}
	Scratch string
	Profile string
	Now     func() time.Time
}

// dumpTool is the tool versions tool whose run image dumps the database.
const dumpTool = "postgres"

// Where the dump's task finds its connection's files and leaves its result.
const (
	dumpPassFile = "/apptown/pg/pgpass"
	dumpRootCert = "/apptown/pg/root.crt"
	// dumpSystemRoots is the pinned image's system roots, which trust.Run
	// extends with the authorities AppTown's containers trust.
	dumpSystemRoots = "/etc/ssl/certs/ca-certificates.crt"
)

// dumpConn is how the dump's task reaches the database: libpq's
// environment (no password), the generated files it names, and the network.
type dumpConn struct {
	env     []string
	files   []execution.File
	network string
}

// client is the image that dumps the server and the connection it is given.
func (d databaseDump) client(ctx context.Context) (string, dumpConn, error) {
	if _, err := d.DB.OwnServer(ctx); err != nil {
		return "", dumpConn{}, err
	}
	image, ok := d.Tools.Current().RunImage(dumpTool, strconv.Itoa(database.OwnPostgresMax))
	if !ok {
		return "", dumpConn{}, fmt.Errorf("this AppTown's tool versions pin no PostgreSQL %d image, whose pg_dump a backup dumps with", database.OwnPostgresMax)
	}
	if d.URL != "" {
		if loopback(d.URL) {
			return "", dumpConn{}, errors.New("the -db-url names a loopback address, which inside the backup's pg_dump container is the container itself; name an address that container can reach")
		}
		conn, err := libpqConn(d.URL)
		return image, conn, err
	}
	if d.Place.Results != nil {
		return "", dumpConn{}, errors.New("the built-in database is on this AppTown's own Docker, and its dumps run on a worker")
	}
	names, raw, ok := readBuiltinDB(d.DataDir)
	if !ok {
		return "", dumpConn{}, errors.New("db.env names no built-in database container, volume or database")
	}
	conn, err := libpqConn(raw)
	if err != nil {
		return "", dumpConn{}, err
	}
	// Inside the database container's own network namespace the server
	// is on 127.0.0.1:5432, wherever apptownd itself runs.
	for i, kv := range conn.env {
		switch {
		case strings.HasPrefix(kv, "PGHOST="):
			conn.env[i] = "PGHOST=127.0.0.1"
		case strings.HasPrefix(kv, "PGPORT="):
			conn.env[i] = "PGPORT=5432"
		}
	}
	conn.network = "container:" + names.Container
	return image, conn, nil
}

// loopback says whether a postgres:// URL's host is this machine's
// loopback, by name or address.
func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Dump writes a whole gzipped dump at path, or nothing: it is published
// only after the task exited 0 (pg_dump and gzip both, under pipefail) and
// its result was written and synced in full.
func (d databaseDump) Dump(ctx context.Context, path string) error {
	image, conn, err := d.client(ctx)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".apptown-dump-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	script := "pg_dump --no-owner --no-privileges | gzip -6 > " + execution.StateRoot + "/dump.sql.gz"
	if err := d.Place.run(ctx, image, conn, script, "dump.sql.gz", file); err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// DumpClientVersion is the pg_dump that Dump runs, as `pg_dump --version`
// says it in a task of the same image, where Dump's runs.
func (d databaseDump) DumpClientVersion(ctx context.Context) (string, error) {
	image, conn, err := d.client(ctx)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := d.Place.run(ctx, image, dumpConn{network: conn.network}, "pg_dump --version > "+execution.StateRoot+"/version", "version", &out); err != nil {
		return "", fmt.Errorf("pg_dump --version: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

// run runs script in a task of image with conn, under bash's pipefail after
// AppTown's trusted authorities are installed (trust.Run), and copies the
// file it leaves in its state folder as result to out. A task that exits
// other than 0, or leaves no such file, is an error with its output. The
// task is released whatever happened.
func (p *dumpPlace) run(ctx context.Context, image string, conn dumpConn, script, result string, out io.Writer) (err error) {
	now := clock.Or(p.Now)()
	key := "apptown-dump-" + ids.New(now).String()
	expires := now.Add(time.Hour)
	if deadline, ok := ctx.Deadline(); ok {
		expires = deadline
	}
	spec := execution.TaskSpec{Key: key, Image: image, Env: conn.env, Files: conn.files, Network: conn.network,
		Argv:      []string{"bash", "-o", "pipefail", "-c", trust.Run + "; " + script},
		Admission: execution.Admission{Profile: p.Profile, Network: execution.NetworkDefault, Expires: expires}}
	local := ""
	if p.Results != nil {
		spec.Results = []execution.Result{{Name: result, Path: execution.StateRoot + "/" + result, Max: execution.MaxResultBytes, Fresh: true}}
	} else {
		if err := os.MkdirAll(p.Scratch, 0o700); err != nil {
			return err
		}
		if local, err = os.MkdirTemp(p.Scratch, "dump-"); err != nil {
			return err
		}
		defer os.RemoveAll(local)
		spec.Mounts = []string{local + ":" + execution.StateRoot}
	}
	// Released whatever happened, from before it is ensured (an ensure that
	// fails or is cancelled may still have made the task or staged its
	// files), on a context of its own: a failed or cancelled dump leaves no
	// task behind.
	ref := execution.TaskRef{Key: key}
	defer func() {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_ = p.Tasks.Stop(settle, ref)
		if releaseErr := p.Tasks.Release(settle, ref); releaseErr != nil && err == nil {
			err = fmt.Errorf("releasing the dump's task: %w", releaseErr)
		}
	}()
	obs, err := p.Tasks.Ensure(ctx, spec)
	if err != nil {
		return err
	}
	ref.ProviderID = obs.ID
	if err := p.Tasks.Start(ctx, ref); err != nil {
		return err
	}
	code, err := p.Tasks.Wait(ctx, ref)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("exit %d: %s", code, p.said(ctx, ref))
	}
	if p.Results == nil {
		f, err := os.Open(filepath.Join(local, result))
		if err != nil {
			return fmt.Errorf("the task left no %s: %w", result, err)
		}
		defer f.Close()
		_, err = io.Copy(out, f)
		return err
	}
	files, err := p.Results.Results(ctx, ref)
	if err != nil {
		return err
	}
	switch f := files[result]; f.State {
	case execution.ResultPresent:
		_, err = out.Write(f.Content)
		return err
	case execution.ResultTooLarge:
		return fmt.Errorf("the gzipped dump is %d bytes, over the %d a task on a worker may leave", f.Size, execution.MaxResultBytes)
	default:
		return fmt.Errorf("the task left no %s (%s %s)", result, f.State, f.Reason)
	}
}

// said is the end of what a task printed, for its failure.
func (p *dumpPlace) said(ctx context.Context, ref execution.TaskRef) string {
	page, err := p.Tasks.Output(ctx, ref, execution.OutputRead{Max: 64 << 10})
	if err != nil {
		return "its output could not be read: " + err.Error()
	}
	said := strings.TrimSpace(string(page.Data))
	if len(said) > 300 {
		said = "..." + said[len(said)-300:]
	}
	return said
}

// databaseBackup owns the instance backup effect, independently of job delivery.
// Its settings are captured during construction; no worker retains App.
type databaseBackup struct {
	db      *database.DB
	dump    databaseDump
	dataDir string
	enabled bool
	now     func() time.Time // AppTown's clock, which dates the day's dump
}

func (b databaseBackup) Run(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	if !b.enabled {
		return map[string]any{"skipped": "AppTown's database is not the built-in one"}, nil
	}
	if err := b.take(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"took": "today's dump, unless it already existed"}, nil
}

const builtinDBBackupKeep = 14

// take dumps the built-in database into <dataDir>/backups as a
// gzipped SQL file, keeps the last fourteen, and audits the dump. Run by
// the scheduler once a day (design/subsystems/postgres-migration.md §3.8). A restore
// is `gunzip -c file | docker exec -i <APPTOWN_DB_CONTAINER> psql -U postgres <database>`
// into an empty database (the one APPTOWN_DB_URL names).
func (b databaseBackup) take(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if !b.enabled {
		return nil
	}
	dir := dataprotection.DiskDirectory(b.dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("database backup: %v", err)
		return err
	}
	name := filepath.Join(dir, "apptown-"+clock.Or(b.now)().UTC().Format("2006-01-02")+".sql.gz")
	if _, err := os.Stat(name); err == nil {
		return nil // today's is done
	}
	if err := b.dump.Dump(ctx, name); err != nil {
		audit.At(clock.Or(b.now)()).BestEffort(ctx, b.db, "apptown-system", ids.Nil, "db.backup-failed", name, map[string]any{"error": err.Error()})
		return err
	}
	fi, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("database backup %s: reading what was written: %w", name, err)
	}
	// The backup's record is its evidence: one not recorded fails the job,
	// though the file stays (it is a good backup).
	if err := audit.At(clock.Or(b.now)()).Record(context.WithoutCancel(ctx), b.db, "apptown-system", ids.Nil, "db.backup", name, map[string]any{"bytes": fi.Size(), "keep": builtinDBBackupKeep}); err != nil {
		return fmt.Errorf("database backup %s was written, but its record was not: %w", name, err)
	}
	// Keep the newest N, by the day each is of: a release before 1.0 named
	// them forge-<day>, and those age out under the same rule.
	var entries []string
	for _, p := range []string{"apptown-", "forge-"} {
		found, _ := filepath.Glob(filepath.Join(dir, p+"*.sql.gz"))
		entries = append(entries, found...)
	}
	day := func(path string) string {
		base := filepath.Base(path)
		return strings.TrimPrefix(strings.TrimPrefix(base, "apptown-"), "forge-")
	}
	sort.Slice(entries, func(i, j int) bool { return day(entries[i]) < day(entries[j]) })
	if len(entries) > builtinDBBackupKeep {
		for _, old := range entries[:len(entries)-builtinDBBackupKeep] {
			_ = os.Remove(old)
		}
	}
	return nil
}

// libpqConn is a postgres:// URL as libpq's environment, with the password
// in a generated password file and a root certificate the URL names as a
// generated file. A URL part it does not know is refused rather than
// dropped; sslmode is passed on as written.
func libpqConn(raw string) (dumpConn, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return dumpConn{}, errors.New("the -db-url is not a postgres:// URL")
	}
	var c dumpConn
	c.env = []string{"PGHOST=" + u.Hostname(), "PGDATABASE=" + strings.TrimPrefix(u.Path, "/")}
	if p := u.Port(); p != "" {
		c.env = append(c.env, "PGPORT="+p)
	}
	if u.User != nil {
		c.env = append(c.env, "PGUSER="+u.User.Username())
		if pw, ok := u.User.Password(); ok {
			// One line for every connection, escaped as libpq reads it.
			line := "*:*:*:*:" + strings.NewReplacer(`\`, `\\`, ":", `\:`).Replace(pw) + "\n"
			c.files = append(c.files, execution.File{Path: dumpPassFile, Content: []byte(line), Mode: 0o600})
			c.env = append(c.env, "PGPASSFILE="+dumpPassFile)
		}
	}
	query := u.Query()
	mode := query.Get("sslmode")
	for k, v := range query {
		switch k {
		case "sslmode":
			c.env = append(c.env, "PGSSLMODE="+v[0])
		case "sslrootcert":
			// The file is AppTown's (it connects with it); the task is given
			// its bytes.
			pem, err := os.ReadFile(v[0])
			if err != nil {
				return dumpConn{}, fmt.Errorf("reading the -db-url's sslrootcert: %w", err)
			}
			c.files = append(c.files, execution.File{Path: dumpRootCert, Content: pem, Mode: 0o644})
			c.env = append(c.env, "PGSSLROOTCERT="+dumpRootCert)
		default:
			return dumpConn{}, fmt.Errorf("the -db-url's %q setting is not one a backup passes to pg_dump", k)
		}
	}
	// Verifying the server with no root certificate named: the system's
	// roots, as AppTown's own connection verifies it, with the authorities
	// AppTown's containers trust added (trust.Run).
	if (mode == "verify-ca" || mode == "verify-full") && !query.Has("sslrootcert") {
		c.env = append(c.env, "PGSSLROOTCERT="+dumpSystemRoots)
	}
	return c, nil
}
