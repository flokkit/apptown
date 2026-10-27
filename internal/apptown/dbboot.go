package apptown

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/datalayout"
	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/poll"
)

// The built-in database (design/subsystems/postgres-migration.md §2.2, §3.9): when no
// -db-url is configured, apptownd runs its own Postgres container on this
// box and keeps the bootstrap in files the way secrets.env is kept —
// because AppTown must reach its database before it can read anything
// from it. Zero setup stays zero setup; a real Postgres is one flag away.
//
//   <dataDir>/db.env        APPTOWN_DB_URL for the container (mode 0600), and
//                           the container's and volume's names
//                           (APPTOWN_DB_CONTAINER, APPTOWN_DB_VOLUME)
//   <dataDir>/instance-id   this installation's id; the container and the
//                           volume carry it as a label and are refused
//                           when it differs
//   <dataDir>/pgdata        the bind location on Linux, on the same
//                           dataset the data directory lives on; fresh native
//                           Mac installs keep a receipt here and bytes in Docker

// builtinDBNames are the built-in database's container and volume, as
// db.env records them, and the database inside, as its URL names it: an
// installation keeps the names it was made with, so its data is always found
// where it is (an install made before 1.0 keeps the names it was made with).
type builtinDBNames struct{ Container, Volume, Database string }

var (
	// currentBuiltinDB names a built-in database made now.
	currentBuiltinDB = builtinDBNames{Container: "apptown-db", Volume: "apptown-data-apptown-db", Database: "apptown"}
)

const (
	builtinDBImage = "postgres:16-alpine"
	builtinDBPort  = "54321"
)

// EnsureBuiltinDB starts (or finds) the built-in container and returns
// the URL to reach it, once it answers or ctx ends. The password is minted
// once into db.env.
func EnsureBuiltinDB(ctx context.Context, dataDir string) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", errors.New("no -db-url and docker is not available: AppTown's database is Postgres; pass -db-url or install docker for the built-in one")
	}
	url, password, names, err := builtinDBCredentials(dataDir)
	if err != nil {
		return "", err
	}
	instance, err := instanceID(dataDir)
	if err != nil {
		return "", err
	}
	return ensureBuiltinDB(ctx, docker.Scope{Client: docker.CLI{}}, dataDir, url, password, instance, names, waitForDB)
}

// ensureBuiltinDB is EnsureBuiltinDB's Docker part, with the daemon and the
// wait for the database given, so a test can run it against a fake.
func ensureBuiltinDB(ctx context.Context, dk docker.Scope, dataDir, url, password, instance string, names builtinDBNames, waitForDB func(context.Context, string) error) (string, error) {
	return ensureBuiltinDBOnOS(ctx, dk, dataDir, url, password, instance, names, waitForDB, runtime.GOOS)
}

func ensureBuiltinDBOnOS(ctx context.Context, dk docker.Scope, dataDir, url, password, instance string, names builtinDBNames, waitForDB func(context.Context, string) error, goos string) (string, error) {
	there, err := dk.Exists(ctx, "inspect", names.Container)
	if err != nil {
		return "", err
	}
	running := false
	if there {
		out, err := dk.Run(ctx, "inspect", "-f", `{{.State.Running}}`+"\t"+`{{index .Config.Labels "`+dockerlabel.Instance+`"}}`+"\t"+`{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Type}}:{{.Name}}{{end}}{{end}}`, names.Container)
		if err != nil {
			return "", fmt.Errorf("reading the database container: %w", err)
		}
		fields := strings.Split(strings.TrimSpace(string(out)), "\t")
		if len(fields) != 3 || fields[1] != instance || (fields[0] != "true" && fields[0] != "false") {
			return "", fmt.Errorf("container %s has no matching installation identity (%s); inspect its ownership or use -db-url", names.Container, instance)
		}
		if fields[2] != "volume:"+names.Volume {
			return "", fmt.Errorf("container %s does not use its recorded database volume %s; preserve both stores and recover their configuration", names.Container, names.Volume)
		}
		running = fields[0] == "true"
	}
	location, err := prepareBuiltinStorage(ctx, dk, dataDir, instance, names, goos, there)
	if err != nil {
		return "", err
	}
	if there {
		if !running {
			if _, err := dk.Run(ctx, "start", names.Container); err != nil {
				return "", fmt.Errorf("starting the existing database container: %w", err)
			}
		}
		return url, waitForDB(ctx, url)
	}
	args := []string{"--name", names.Container, "--restart", "unless-stopped",
		"-p", "127.0.0.1:" + builtinDBPort + ":5432",
		"-v", names.Volume + ":/var/lib/postgresql/data",
		"-e", "POSTGRES_PASSWORD=" + password, "-e", "POSTGRES_DB=" + names.Database,
		"-e", "POSTGRES_INITDB_ARGS=--locale=C.UTF-8",
		"--label", dockerlabel.Class + "=infra", "--label", dockerlabel.Owner + "=" + names.Container, "--label", dockerlabel.Instance + "=" + instance,
		builtinDBImage,
		// Evidence for indexes, with the database's parameters —
		// credentials, private records — kept out of the log: statements over 250ms are logged, their values never.
		"-c", "log_parameter_max_length=0", "-c", "log_parameter_max_length_on_error=0",
		"-c", "log_min_duration_statement=250"}
	if err := dk.EnsureRunning(ctx, names.Container, args); err != nil {
		return "", fmt.Errorf("starting the built-in database: %w", err)
	}
	log.Printf("built-in database: started %s (%s) with its data in %s", names.Container, builtinDBImage, location)
	return url, waitForDB(ctx, url)
}

func builtinDBCredentials(dataDir string) (url, password string, names builtinDBNames, err error) {
	path := filepath.Join(dataDir, "db.env")
	raw, err := os.ReadFile(path)
	if err == nil {
		// Each key is required: db.env holds the only copy of the database's
		// password, and its names say where the data is. A file missing one
		// is refused as it is, never rewritten or replaced.
		values := map[string]string{}
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				values[k] = v
			}
		}
		url, password = values["APPTOWN_DB_URL"], values["APPTOWN_DB_PASSWORD"]
		names = builtinDBNames{Container: values["APPTOWN_DB_CONTAINER"], Volume: values["APPTOWN_DB_VOLUME"], Database: databaseInURL(url)}
		var missing []string
		for _, k := range []string{"APPTOWN_DB_URL", "APPTOWN_DB_PASSWORD", "APPTOWN_DB_CONTAINER", "APPTOWN_DB_VOLUME"} {
			if values[k] == "" {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			return "", "", names, fmt.Errorf("%s is present but incomplete; it needs %s", path, strings.Join(missing, ", "))
		}
		return url, password, names, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		// Unreadable is not absent: minting would replace the only copy of
		// an existing database's password.
		return "", "", names, fmt.Errorf("reading %s: %w", path, err)
	}
	names = currentBuiltinDB
	pw := make([]byte, 24)
	if _, err := rand.Read(pw); err != nil {
		return "", "", names, err
	}
	password = hex.EncodeToString(pw)
	url = fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/%s", password, builtinDBPort, names.Database)
	body := "# AppTown's built-in database.\n" +
		"# Minted once by apptownd. If this file is lost while the data volume\n" +
		"# survives, reset the password inside the container (docker exec\n" +
		"# " + names.Container + " psql -U postgres -c \"ALTER USER postgres PASSWORD '...'\")\n" +
		"# and write it here.\n" +
		"APPTOWN_DB_URL=" + url + "\nAPPTOWN_DB_PASSWORD=" + password + "\n" +
		"APPTOWN_DB_CONTAINER=" + names.Container + "\nAPPTOWN_DB_VOLUME=" + names.Volume + "\n"
	if existed, err := durable.Write(dataDir, "db.env", []byte(body), 0o600, true); err != nil || existed {
		if existed {
			err = fmt.Errorf("%s appeared while minting; start again to read it", path)
		}
		return "", "", names, err
	}
	return url, password, names, nil
}

// builtinDBFor is an AppTown's built-in database's names; zero when it
// runs on -db-url.
func builtinDBFor(usingBuiltin bool, dataDir string) builtinDBNames {
	if !usingBuiltin {
		return builtinDBNames{}
	}
	n, _ := readBuiltinDBNames(dataDir)
	return n
}

// readBuiltinDBNames is what db.env says the built-in database is called;
// false when there is no built-in database's db.env to read (an AppTown on
// -db-url), or it names none.
func readBuiltinDBNames(dataDir string) (builtinDBNames, bool) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "db.env"))
	if err != nil {
		return builtinDBNames{}, false
	}
	var n builtinDBNames
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "APPTOWN_DB_CONTAINER":
			n.Container = v
		case "APPTOWN_DB_VOLUME":
			n.Volume = v
		case "APPTOWN_DB_URL":
			n.Database = databaseInURL(v)
		}
	}
	return n, n.Container != "" && n.Volume != "" && n.Database != ""
}

// databaseInURL is the database a postgres:// URL names; empty when it
// names none or does not parse.
func databaseInURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

func instanceID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "instance-id")
	if raw, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw)), nil
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	return id, os.WriteFile(path, []byte(id+"\n"), 0o644)
}

func waitForDB(ctx context.Context, url string) error {
	var err error
	if poll.Until(ctx, 90*time.Second, 500*time.Millisecond, func() bool {
		var db *sql.DB
		if db, err = database.OpenRaw(url); err == nil {
			err = db.Ping()
			_ = db.Close()
		}
		return err == nil
	}) {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("the database had not answered when the wait ended: %w (last: %v)", ctx.Err(), err)
	}
	return fmt.Errorf("the database never answered: %w", err)
}

// DataLayoutVersion is the layout of the data directory this build reads
// and writes. A release that changes where its files live raises it and
// adds the step that moves a directory from the previous one; the upgrade
// test's fixtures carry the file, so a layout step is tested like a
// migration.
const DataLayoutVersion = datalayout.Version

// ErrNewerDataLayout: the data directory was written by a newer build, whose
// files this build would misread. Downgrades are not supported.
var ErrNewerDataLayout = datalayout.ErrNewerLayout

// PrepareDataLayout validates without stamping. UpgradeDataLayout performs the
// checked transition under daemon exclusion and stamps only after verification.
func PrepareDataLayout(dataDir string) error {
	_, err := datalayout.ReadVersion(dataDir)
	return err
}

// BuiltinDatabase is the built-in database of the AppTown whose data
// directory is DataDir, on Docker, as a whole-AppTown backup and restore
// use it (instancebackup.BuiltinDatabase).
type BuiltinDatabase struct {
	DataDir string
	Docker  docker.Scope
}

// Names is what db.env says the container and the database are.
func (b BuiltinDatabase) Names() (container, database string, ok bool) {
	n, ok := readBuiltinDBNames(b.DataDir)
	return n.Container, n.Database, ok
}

// Dump writes a whole gzipped dump of the database at path, or nothing.
func (b BuiltinDatabase) Dump(ctx context.Context, path string) error {
	n, ok := readBuiltinDBNames(b.DataDir)
	if !ok {
		return errors.New("db.env names no built-in database container, volume or database")
	}
	return dumpBuiltinDatabase(ctx, b.Docker, n, path)
}

// Ensure starts or makes the database and answers its URL.
func (b BuiltinDatabase) Ensure(ctx context.Context) (string, error) {
	return EnsureBuiltinDB(ctx, b.DataDir)
}

// DumpClientVersion observes the client in the container used by Dump.
func (b BuiltinDatabase) DumpClientVersion(ctx context.Context) (string, error) {
	container, _, ok := b.Names()
	if !ok {
		return "", errors.New("db.env names no built-in database")
	}
	raw, err := b.Docker.ExecResult(ctx, container, containerresult.Spec{Script: "pg_dump --version"})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// The receipt lives under pgdata so logical instance archives never import a
// source host's volume identity. It is durable before PostgreSQL can write.
const builtinStorageReceipt = ".apptown-docker-volume"

type builtinStorageBinding struct {
	Version   int       `json:"version"`
	Instance  string    `json:"instance"`
	Volume    string    `json:"volume"`
	CreatedAt time.Time `json:"createdAt"`
}

type builtinVolume struct {
	Name      string
	CreatedAt time.Time
	Driver    string
	Options   map[string]string
	Labels    map[string]string
}

func readBuiltinStorage(dataDir, instance, volume string) (*builtinStorageBinding, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "pgdata", builtinStorageReceipt))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading database storage receipt: %w", err)
	}
	var b builtinStorageBinding
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("reading database storage receipt: %w", err)
	}
	if b.Version != 1 || b.Instance != instance || b.Volume != volume || b.CreatedAt.IsZero() {
		return nil, errors.New("database storage receipt does not match this installation; preserve it and recover the recorded database")
	}
	return &b, nil
}

func inspectBuiltinVolume(ctx context.Context, dk docker.Scope, instance, name string) (builtinVolume, error) {
	var v builtinVolume
	raw, err := dk.Run(ctx, "volume", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return v, fmt.Errorf("reading database volume: %w", err)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("reading database volume: %w", err)
	}
	if v.Name != name || v.Driver == "" {
		return v, errors.New("database volume observation is incomplete")
	}
	if v.Labels[dockerlabel.Instance] != instance {
		return v, fmt.Errorf("volume %s has no matching installation label (observed %q, expected %s); inspect its ownership or give this AppTown a separate database with -db-url", name, v.Labels[dockerlabel.Instance], instance)
	}
	return v, nil
}

func emptyBuiltinDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading retained database files: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("retained database files in %s cannot be replaced with a new Docker volume; preserve them and recover the database explicitly or use -db-url", dir)
	}
	return nil
}

func bindBuiltinStorage(dataDir, instance string, v builtinVolume, prior *builtinStorageBinding) error {
	if v.Driver != "local" || len(v.Options) != 0 || v.CreatedAt.IsZero() {
		return errors.New("database volume is not an observed Docker-native local volume; preserve it and recover its storage configuration")
	}
	b := builtinStorageBinding{Version: 1, Instance: instance, Volume: v.Name, CreatedAt: v.CreatedAt.UTC()}
	dir := filepath.Join(dataDir, "pgdata")
	if prior != nil {
		if prior.Volume != b.Volume || !prior.CreatedAt.Equal(b.CreatedAt) {
			return errors.New("the recorded database volume was replaced; preserve the storage receipt and recover the original database")
		}
	} else {
		if err := emptyBuiltinDirectory(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := durable.SyncDir(dataDir); err != nil {
			return err
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		existed, err := durable.Write(dir, builtinStorageReceipt, raw, 0o600, true)
		if err != nil {
			return fmt.Errorf("recording database storage before startup: %w", err)
		}
		if existed {
			found, err := readBuiltinStorage(dataDir, instance, v.Name)
			if err != nil {
				return err
			}
			if found == nil || !found.CreatedAt.Equal(b.CreatedAt) {
				return errors.New("database storage changed while recording it; inspect the stored receipt before starting")
			}
		}
	}
	// Also settle a receipt whose prior writer stopped before directory sync.
	if err := durable.SyncDir(dataDir); err != nil {
		return err
	}
	return durable.SyncDir(dir)
}

func prepareBuiltinStorage(ctx context.Context, dk docker.Scope, dataDir, instance string, names builtinDBNames, goos string, containerExists bool) (string, error) {
	prior, err := readBuiltinStorage(dataDir, instance, names.Volume)
	if err != nil {
		return "", err
	}
	exists, err := dk.Exists(ctx, "volume", "inspect", names.Volume)
	if err != nil {
		return "", err
	}
	pgdata := filepath.Join(dataDir, "pgdata")
	if !exists {
		if prior != nil || containerExists {
			return "", fmt.Errorf("recorded database volume %s is missing; preserve the data directory and recover the original database", names.Volume)
		}
		args := []string{"volume", "create", "--driver", "local"}
		if goos == "darwin" {
			if err := emptyBuiltinDirectory(pgdata); err != nil {
				return "", err
			}
		} else {
			if err := os.MkdirAll(pgdata, 0o700); err != nil {
				return "", err
			}
			args = append(args, "--opt", "type=none", "--opt", "o=bind", "--opt", "device="+pgdata)
		}
		args = append(args, "--label", dockerlabel.Class+"=infra", "--label", dockerlabel.Owner+"="+names.Container, "--label", dockerlabel.Instance+"="+instance, names.Volume)
		if _, err := dk.Run(ctx, args...); err != nil {
			return "", fmt.Errorf("creating the database volume: %w", err)
		}
	}
	v, err := inspectBuiltinVolume(ctx, dk, instance, names.Volume)
	if err != nil {
		return "", err
	}
	if v.Driver == "local" && len(v.Options) == 0 {
		if err := bindBuiltinStorage(dataDir, instance, v, prior); err != nil {
			return "", err
		}
		return "Docker volume " + v.Name, nil
	}
	if prior != nil {
		return "", errors.New("the recorded Docker-native database volume now has different storage options; recover its original storage before starting")
	}
	if v.Driver != "local" || v.Options["type"] != "none" || v.Options["o"] != "bind" || v.Options["device"] == "" {
		return "", errors.New("database volume has unsupported storage options; preserve it and recover its original configuration")
	}
	return v.Options["device"], nil
}
