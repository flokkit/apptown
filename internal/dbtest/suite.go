// Package dbtest supplies isolated PostgreSQL clones for database-backed tests.
// A suite owns its template and accepts initialization from the schema's owner.
package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/poll"
)

var templateSequence atomic.Uint64

// closeWithoutWaiting dials the test server with SO_LINGER 0, so closing a
// connection resets it rather than leaving this side in TIME_WAIT. Every
// test opens and closes its own database's connections; a gate's packages
// together closed thousands a minute, and macOS (16,384 local ports, 30 s
// in TIME_WAIT) ran out of local ports: connecting to the test server, and
// to in-process workers, failed with "can't assign requested address". A
// pool closes a connection only when it is done with it, so the reset loses
// nothing; the server ends that backend.
func closeWithoutWaiting(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{KeepAlive: 5 * time.Minute}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := tcp.SetLinger(0); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

const container = "forge-test-db"
const port = "54329"

type Suite struct {
	once                     sync.Once
	initialize               func(context.Context, string) (*database.DB, error)
	adminURL, base, template string
	// params follow each database name in a connection URL.
	params string
	err    error
	count  atomic.Int64
	// admin is opened once and shared. Every clone and every drop used to
	// open its own pool, so a test cost three connection establishments
	// rather than one. On a Mac that traffic crosses Docker's port
	// forwarder, which drops connections under that rate long before
	// PostgreSQL reaches max_connections: the suite failed in the hundreds
	// while only ~58 connections were live on the server.
	admin *sql.DB
	// Dropping a clone costs about as much as making it (on the private
	// test server: 0.66 s drop, 0.76 s clone, both forcing a checkpoint). A suite run through Run drops clones in the
	// background so a finished test frees its parallel slot at once, unless
	// drops have fallen behind (waitingDrops); Close
	// waits for every drop and reports any that failed.
	background bool
	drops      chan string
	dropping   sync.WaitGroup
	dropMu     sync.Mutex
	dropErrs   []error
}

// dropWorkers is how many clones are dropped at once; the admin pool holds four.
const dropWorkers = 2

// waitingDrops is how many finished tests' clones may wait for a drop worker.
// Beyond it, a finished test's cleanup waits its turn, so a package makes
// clones no faster than it drops them. Each root clone is about 12 MB of the
// private server's 3.9 GB tmpfs. On 11 October a full gate's packages queued
// drops faster than the server finished them: when the tmpfs filled, three
// binaries each held about 64 undropped clones (the queue's old capacity),
// the server restarted, and their last drops failed, leaving 301 databases.
const waitingDrops = dropWorkers

func NewSuite(initialize func(context.Context, string) (*database.DB, error)) *Suite {
	return &Suite{initialize: initialize}
}

func (s *Suite) New(t testing.TB) *database.DB {
	t.Helper()
	s.once.Do(s.setup)
	if s.err != nil {
		t.Fatalf("test database: %v", s.err)
	}
	name := fmt.Sprintf("%s_t%d", s.template, s.count.Add(1))
	// Copy the template files instead of logging every copied page. On the
	// disposable test server this reduces WAL and subsequent checkpoint work;
	// each test still owns a complete database, dropped synchronously below.
	if _, err := s.admin.Exec(fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s STRATEGY FILE_COPY`, name, s.template)); err != nil {
		t.Fatalf("clone template: %v", err)
	}
	var db *database.DB
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Errorf("closing test database: %v", err)
			}
		}
		if s.background {
			s.drops <- name
			return
		}
		if err := s.drop(name); err != nil {
			t.Errorf("dropping test database: %v", err)
		}
	})
	// The template already carries schema and seeds. Re-running application
	// initialization on every identical clone is unnecessary work and coupling.
	opened, err := database.OpenWith(s.base+name+s.params, closeWithoutWaiting)
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	db = opened
	return db
}

func (s *Suite) setup() {
	ctx := context.Background() // a test suite is its own entry point
	base := os.Getenv("FORGE_TEST_DB_URL")
	if base == "" {
		if err := startServer(ctx); err != nil {
			s.err = err
			return
		}
		base = "postgres://postgres:forge@127.0.0.1:" + port + "/"
		// The private server is local and disposable, so it is reached
		// without TLS. Otherwise every connection first tries a TLS
		// handshake across Docker's port forwarder, which drops it under
		// parallel load ("tls error: EOF") and doubled the handshakes.
		s.params = "?sslmode=disable"
	}
	if i := strings.LastIndex(base, "/"); i > 8 && !strings.HasSuffix(base, "/") {
		base = base[:i+1]
	}
	s.base, s.adminURL = base, base+"postgres"+s.params
	cfg, err := pgx.ParseConfig(s.adminURL)
	if err != nil {
		s.err = err
		return
	}
	cfg.DialFunc = closeWithoutWaiting
	admin := stdlib.OpenDB(*cfg)
	// Held for the life of the test binary: clones and drops share it, and
	// its connections stay open between them rather than being closed and
	// made again (database/sql keeps two idle unless told otherwise).
	admin.SetMaxOpenConns(4)
	admin.SetMaxIdleConns(4)
	s.admin = admin
	if s.background {
		s.drops = make(chan string, waitingDrops)
		for range dropWorkers {
			s.dropping.Add(1)
			go s.dropInBackground()
		}
	}
	if !poll.Until(context.Background(), time.Minute, 300*time.Millisecond, func() bool {
		err = admin.Ping()
		return err == nil
	}) {
		s.err = fmt.Errorf("test postgres never answered: %w", err)
		return
	}
	if s.params != "" {
		s.sweepOrphans()
	}
	s.template = fmt.Sprintf("forge_tpl_%d_%d_%d", os.Getpid(), time.Now().UnixNano(), templateSequence.Add(1))
	if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %s`, s.template)); err != nil {
		s.err = fmt.Errorf("create template (the account needs CREATEDB): %w", err)
		return
	}
	template, err := s.initialize(ctx, base+s.template)
	if err != nil {
		s.err = fmt.Errorf("initialize template: %w", err)
		return
	}
	// PostgreSQL will not clone a template with active connections.
	s.err = template.Close()
}

// Multiple test packages may start together. Never remove a container another
// package could just have started; a name conflict means inspect/reuse it.
// The settings below apply when the container is created. A container made
// by an earlier version of this file keeps its old settings until someone
// removes it once (`docker rm -f forge-test-db`); the harness never does.
func startServer(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is unavailable and FORGE_TEST_DB_URL is not set")
	}
	err := docker.Scope{Client: docker.CLI{}}.EnsureRunning(ctx, container, []string{"-d", "--name", container, "-p", "127.0.0.1:" + port + ":5432",
		"-e", "POSTGRES_PASSWORD=forge", "-e", "POSTGRES_INITDB_ARGS=--locale=C.UTF-8",
		"--label", dockerlabel.Class + "=infra", "--label", dockerlabel.Owner + "=forge-test-db",
		// The data directory lives in memory. Every test clones a template
		// database and drops it, and nothing in it outlives the test binary;
		// a stopped container comes back empty and initialises again.
		"--tmpfs", "/var/lib/postgresql/data",
		"postgres:16-alpine",
		// Every cloned test database opens its own pool of up to ten
		// connections (internal/database), and `go test ./...` runs a package
		// per core with parallel tests inside each. Postgres' default of 100
		// is a few packages' worth: the suite exhausted it and hundreds of
		// tests failed mid-query, which reads like a crashed server rather
		// than a limit. 800 is far more than the suite can reach.
		"-c", "max_connections=800",
		// No durability. This server exists to be thrown away, and every
		// clone, drop and commit was paying for crash safety on a
		// virtualised disk (87 ms per clone and drop with fsync on). Minimal WAL needs no senders.
		"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off",
		"-c", "wal_level=minimal", "-c", "max_wal_senders=0",
		"-c", "shared_buffers=256MB"})
	if err != nil {
		return fmt.Errorf("starting test postgres: %w", err)
	}
	return nil
}

func (s *Suite) dropInBackground() {
	defer s.dropping.Done()
	for name := range s.drops {
		if err := s.drop(name); err != nil {
			s.dropMu.Lock()
			s.dropErrs = append(s.dropErrs, fmt.Errorf("dropping %s: %w", name, err))
			s.dropMu.Unlock()
		}
	}
}

// sweepOrphans drops the databases of test binaries that are gone. A binary
// that is killed or times out never drops its template or clones, and the
// private server keeps its data in a 4 GB tmpfs, which a few hundred such
// templates fill, and then every package fails with "No space left on
// device". Names carry the creating process's id; only the private server
// on this machine shares that id space, so a shared server is never swept.
// A live process (or a reused id) keeps its databases. The sweep is
// housekeeping: a failure leaves the orphan for the next binary to retry.
func (s *Suite) sweepOrphans() {
	rows, err := s.admin.Query(`SELECT datname FROM pg_database WHERE datname LIKE 'forge_tpl_%'`)
	if err != nil {
		return
	}
	var gone []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(strings.TrimPrefix(name, "forge_tpl_"), "%d_", &pid); err != nil || pid <= 0 {
			continue
		}
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			gone = append(gone, name)
		}
	}
	_ = rows.Close()
	for _, name := range gone {
		_ = s.drop(name)
	}
}

func (s *Suite) drop(name string) error {
	_, err := s.admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
	return err
}

// Run preserves the suite's eight-fixture default; an explicit -parallel wins.
func (s *Suite) Run(m *testing.M) int {
	flag.Parse()
	explicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "test.parallel" {
			explicit = true
		}
	})
	if !explicit {
		_ = flag.Set("test.parallel", "8")
	}
	// Set before any test runs, so every New sees it.
	s.background = true
	code := m.Run()
	if err := s.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "dropping test databases: %v\n", err)
		code = 1
	}

	return code
}

// Close waits for background drops, then releases the suite's template.
func (s *Suite) Close() error {
	if s.drops != nil {
		close(s.drops)
		s.dropping.Wait()
		s.drops = nil
	}
	if s.template == "" {
		return errors.Join(s.dropErrs...)
	}
	return errors.Join(append(s.dropErrs, s.drop(s.template))...)
}
