package forge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/poll"
)

// The built-in database (design/subsystems/postgres-migration.md §2.2, §3.9): when no
// -db-url is configured, forged runs its own Postgres container on this
// box and keeps the bootstrap in files the way secrets.env is kept —
// because the forge must reach its database before it can read anything
// from it. Zero setup stays zero setup; a real Postgres is one flag away.
//
//   <dataDir>/db.env        FORGE_DB_URL for the container (mode 0600)
//   <dataDir>/instance-id   this installation's id; the container and the
//                           volume carry it as a label and are refused
//                           when it differs
//   <dataDir>/pgdata        the volume's bind location, on the same
//                           dataset the data directory lives on

const (
	builtinDBContainer = "forge-db"
	builtinDBVolume    = "forge-data-forge-db"
	builtinDBImage     = "postgres:16-alpine"
	builtinDBPort      = "54321"
	builtinDBName      = "forge"
)

// EnsureBuiltinDB starts (or finds) the built-in container and returns
// the URL to reach it, once it answers or ctx ends. The password is minted
// once into db.env.
func EnsureBuiltinDB(ctx context.Context, dataDir string) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", errors.New("no -db-url and docker is not available: AppTown's database is Postgres; pass -db-url or install docker for the built-in one")
	}
	url, password, err := builtinDBCredentials(dataDir)
	if err != nil {
		return "", err
	}
	instance, err := instanceID(dataDir)
	if err != nil {
		return "", err
	}
	return ensureBuiltinDB(ctx, docker.Scope{Client: docker.CLI{}}, dataDir, url, password, instance, waitForDB)
}

// ensureBuiltinDB is EnsureBuiltinDB's Docker part, with the daemon and the
// wait for the database given, so a test can run it against a fake.
func ensureBuiltinDB(ctx context.Context, dk docker.Scope, dataDir, url, password, instance string, waitForDB func(context.Context, string) error) (string, error) {
	if out, err := dk.Run(ctx, "inspect", "-f", `{{.State.Running}} {{index .Config.Labels "`+dockerlabel.Instance+`"}}`, builtinDBContainer); err == nil {
		fields := strings.Fields(string(out))
		if len(fields) == 2 && fields[1] != instance {
			return "", fmt.Errorf("container %s belongs to another forge installation (instance %s, this one is %s); remove it or point -db-url elsewhere", builtinDBContainer, fields[1], instance)
		}
		if len(fields) >= 1 && fields[0] == "true" {
			return url, waitForDB(ctx, url)
		}
		if out, err := dk.Run(ctx, "start", builtinDBContainer); err == nil {
			return url, waitForDB(ctx, url)
		} else {
			log.Printf("built-in database: start failed (%s); recreating the container", strings.TrimSpace(string(out)))
			dk.Remove(ctx, builtinDBContainer)
		}
	}
	// The volume binds onto the data directory so it sits on the dataset
	// whose snapshots already cover forge's state; a plain named volume
	// would land in Docker's own directory, off the dataset.
	pgdata := filepath.Join(dataDir, "pgdata")
	if err := os.MkdirAll(pgdata, 0o700); err != nil {
		return "", err
	}
	// An existing volume is this installation's only if it carries this
	// installation's id. A leftover from another one binds to that one's
	// pgdata, so using it would run on another forge's database (and fail on
	// its password). forged has always labelled the volume, so an unlabelled
	// one is not this installation's either.
	if out, err := dk.Run(ctx, "volume", "inspect", "-f", `{{index .Labels "`+dockerlabel.Instance+`"}}`+"\t"+`{{index .Options "device"}}`, builtinDBVolume); err == nil {
		// Split before trimming: an unlabelled volume's line starts with the tab.
		owner, folder, _ := strings.Cut(string(out), "\t")
		owner = strings.TrimSpace(owner)
		if owner != instance {
			whose := "has no installation label"
			if owner != "" {
				whose = fmt.Sprintf("belongs to another forge installation (instance %s)", owner)
			}
			return "", fmt.Errorf("volume %s %s, and this one is %s; it keeps its data in %s. If that installation is gone, remove the volume with `docker volume rm %s` (the folder is left as it is) and start again; otherwise give this AppTown its own database with -db-url",
				builtinDBVolume, whose, instance, strings.TrimSpace(folder), builtinDBVolume)
		}
	} else {
		if out, err := dk.Run(ctx, "volume", "create", "--driver", "local",
			"--opt", "type=none", "--opt", "o=bind", "--opt", "device="+pgdata,
			"--label", dockerlabel.Class+"=infra", "--label", dockerlabel.Owner+"="+builtinDBContainer, "--label", dockerlabel.Instance+"="+instance,
			builtinDBVolume); err != nil {
			return "", fmt.Errorf("creating the database volume: %v: %s", err, out)
		}
	}
	args := []string{"-d", "--name", builtinDBContainer, "--restart", "unless-stopped",
		"-p", "127.0.0.1:" + builtinDBPort + ":5432",
		"-v", builtinDBVolume + ":/var/lib/postgresql/data",
		"-e", "POSTGRES_PASSWORD=" + password, "-e", "POSTGRES_DB=" + builtinDBName,
		"-e", "POSTGRES_INITDB_ARGS=--locale=C.UTF-8",
		"--label", dockerlabel.Class + "=infra", "--label", dockerlabel.Owner + "=" + builtinDBContainer, "--label", dockerlabel.Instance + "=" + instance,
		builtinDBImage,
		// Evidence for indexes, with the database's parameters —
		// credentials, private records — kept out of the log: statements over 250ms are logged, their values never.
		"-c", "log_parameter_max_length=0", "-c", "log_parameter_max_length_on_error=0",
		"-c", "log_min_duration_statement=250"}
	if err := dk.EnsureRunning(ctx, builtinDBContainer, args); err != nil {
		return "", fmt.Errorf("starting the built-in database: %w", err)
	}
	log.Printf("built-in database: started %s (%s) with its data under %s", builtinDBContainer, builtinDBImage, pgdata)
	return url, waitForDB(ctx, url)
}

func builtinDBCredentials(dataDir string) (url, password string, err error) {
	path := filepath.Join(dataDir, "db.env")
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "FORGE_DB_URL="); ok {
				url = v
			}
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "FORGE_DB_PASSWORD="); ok {
				password = v
			}
		}
		if url != "" && password != "" {
			return url, password, nil
		}
		return "", "", fmt.Errorf("%s is present but incomplete; it needs FORGE_DB_URL and FORGE_DB_PASSWORD", path)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	password = hex.EncodeToString(raw)
	url = fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/%s", password, builtinDBPort, builtinDBName)
	body := "# AppTown's built-in database (design/subsystems/postgres-migration.md §3.9).\n" +
		"# Minted once by forged. If this file is lost while the data volume\n" +
		"# survives, reset the password inside the container (docker exec\n" +
		"# forge-db psql -U postgres -c \"ALTER USER postgres PASSWORD '...'\")\n" +
		"# and write it here.\n" +
		"FORGE_DB_URL=" + url + "\nFORGE_DB_PASSWORD=" + password + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", "", err
	}
	return url, password, nil
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
