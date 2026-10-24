package apptown

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"io/fs"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
//   <dataDir>/pgdata        the volume's bind location, on the same
//                           dataset the data directory lives on

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
	// The container is this installation's only if it carries this
	// installation's id, as the volume must.
	if out, err := dk.Run(ctx, "inspect", "-f", `{{.State.Running}}`+"\t"+`{{index .Config.Labels "`+dockerlabel.Instance+`"}}`, names.Container); err == nil {
		running, owner, _ := strings.Cut(strings.TrimRight(string(out), "\r\n"), "\t")
		if owner = strings.TrimSpace(owner); owner != instance {
			whose := "has no installation label"
			if owner != "" {
				whose = fmt.Sprintf("belongs to another AppTown installation (instance %s)", owner)
			}
			return "", fmt.Errorf("container %s %s, and this one is %s; remove it or point -db-url elsewhere", names.Container, whose, instance)
		}
		if strings.TrimSpace(running) == "true" {
			return url, waitForDB(ctx, url)
		}
		if out, err := dk.Run(ctx, "start", names.Container); err == nil {
			return url, waitForDB(ctx, url)
		} else {
			log.Printf("built-in database: start failed (%s); recreating the container", strings.TrimSpace(string(out)))
			dk.Remove(ctx, names.Container)
		}
	}
	// The volume binds onto the data directory so it sits on the dataset
	// whose snapshots already cover AppTown's state; a plain named volume
	// would land in Docker's own directory, off the dataset.
	pgdata := filepath.Join(dataDir, "pgdata")
	if err := os.MkdirAll(pgdata, 0o700); err != nil {
		return "", err
	}
	// An existing volume is this installation's only if it carries this
	// installation's id. A leftover from another one binds to that one's
	// pgdata, so using it would run on another AppTown's database (and fail on
	// its password). apptownd has always labelled the volume, so an unlabelled
	// one is not this installation's either.
	if out, err := dk.Run(ctx, "volume", "inspect", "-f", `{{index .Labels "`+dockerlabel.Instance+`"}}`+"\t"+`{{index .Options "device"}}`, names.Volume); err == nil {
		// Split before trimming: an unlabelled volume's line starts with the tab.
		owner, folder, _ := strings.Cut(string(out), "\t")
		owner = strings.TrimSpace(owner)
		if owner != instance {
			whose := "has no installation label"
			if owner != "" {
				whose = fmt.Sprintf("belongs to another AppTown installation (instance %s)", owner)
			}
			return "", fmt.Errorf("volume %s %s, and this one is %s; it keeps its data in %s. If that installation is gone, remove the volume with `docker volume rm %s` (the folder is left as it is) and start again; otherwise give this AppTown its own database with -db-url",
				names.Volume, whose, instance, strings.TrimSpace(folder), names.Volume)
		}
	} else {
		if out, err := dk.Run(ctx, "volume", "create", "--driver", "local",
			"--opt", "type=none", "--opt", "o=bind", "--opt", "device="+pgdata,
			"--label", dockerlabel.Class+"=infra", "--label", dockerlabel.Owner+"="+names.Container, "--label", dockerlabel.Instance+"="+instance,
			names.Volume); err != nil {
			return "", fmt.Errorf("creating the database volume: %v: %s", err, out)
		}
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
	log.Printf("built-in database: started %s (%s) with its data under %s", names.Container, builtinDBImage, pgdata)
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
const DataLayoutVersion = 1

// ErrNewerDataLayout: the data directory was written by a newer build, whose
// files this build would misread. Downgrades are not supported.
var ErrNewerDataLayout = errors.New("the data directory was written by a newer build")

// PrepareDataLayout checks the data directory's layout version
// (<dataDir>/layout) before anything else reads or writes it: absent (a
// directory made before the file existed) or older is brought to this
// build's version (no step yet: version 1 is the 1.0 layout), the same is
// accepted, newer is refused by name.
func PrepareDataLayout(dataDir string) error {
	path := filepath.Join(dataDir, "layout")
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("reading the data directory's layout version: %w", err)
	default:
		v, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return fmt.Errorf("the data directory's layout version %q is not a number", strings.TrimSpace(string(raw)))
		}
		if v > DataLayoutVersion {
			return fmt.Errorf("%w: layout %d, and this build reads %d; start the newer build, or restore the data directory copied before the upgrade", ErrNewerDataLayout, v, DataLayoutVersion)
		}
		if v == DataLayoutVersion {
			return nil
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(DataLayoutVersion)+"\n"), 0o644)
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
