package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// scannerEffects answers commands with report, and model questions with
// answer when one is set. It keeps the image of every command and the model
// of every question it was asked.
type scannerEffects struct {
	t      *testing.T
	report string
	answer string
	exit   int // the exit every run answers with

	mu       sync.Mutex
	images   []string
	models   []string
	requests []execution.Request
}

func (e *scannerEffects) ran(image string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Contains(e.images, image)
}

func (e *scannerEffects) asked(model string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Contains(e.models, model)
}

func (e *scannerEffects) Run(ctx context.Context, req execution.Request) execution.Outcome {
	e.t.Helper()
	e.mu.Lock()
	e.images = append(e.images, req.Image)
	e.requests = append(e.requests, req)
	e.mu.Unlock()
	dst := filepath.Join(req.Workspace, ".apptown", "semgrep.sarif")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(e.report), 0o644); err != nil {
		e.t.Fatal(err)
	}
	// Answered as a runner answers: a normal exit, and the declared report
	// carried back in the outcome.
	out := execution.Outcome{Termination: execution.Exited, ExitCode: e.exit}
	if req.Report != "" {
		out.Report, out.ReportErr = execution.ReadReport(req.Workspace, req.Report)
	}
	return out
}
func (e *scannerEffects) Ask(_ context.Context, call modelcall.Call) (string, *modelcall.Usage, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.models = append(e.models, call.Model)
	if e.answer == "" {
		return "", nil, errors.New("scanner fixtures make no model calls")
	}
	return e.answer, &modelcall.Usage{}, nil
}
func (*scannerEffects) Observe(context.Context, ids.ID, string, string) (conformance.Outcome, string) {
	return conformance.CannotAssess, ""
}

type scannerOwnerFixture struct {
	DB          *database.DB
	ProjectID   ids.ID
	Context     context.Context
	Conformance *conformance.Service
	Scanners    *Scanners
	Daemon      *dockerfake.Daemon
	Effects     *scannerEffects
}

func newScannerOwnerFixture(t *testing.T) scannerOwnerFixture {
	t.Helper()
	db := testDB(t)
	directory := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := directory.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, identity.Scoped(directory, "project"))
	// Created as project creation does: with its adoption at revision zero,
	// nothing adopted.
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
		if err != nil {
			return id, err
		}
		return id, adoption.InitializeIn(t.Context(), tx, id, 0, "", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true})
	service := conformance.New(db, access.Checked{Authority: identity.Scoped(directory, "conformance"), Projects: projects}, noAdoptedChecks{}, nil)
	daemon := dockerfake.New()
	scope := docker.Scope{Client: daemon}
	effects := &scannerEffects{t: t}
	scanners := NewScanners(ScannerDependencies{DB: db, Docker: &scope, Executor: daemonExecutor(scope), Conformance: service,
		Evaluator: NewEvaluator(effects, nil, stock.Versions()), Integrations: integration.NewService(db, integration.Dependencies{Projects: projects}), Dependencies: dependency.NewCatalog(db, nil)})
	return scannerOwnerFixture{DB: db, ProjectID: projectID, Context: ctx, Conformance: service, Scanners: scanners, Daemon: daemon, Effects: effects}
}

// noAdoptedChecks is a check source with nothing adopted: the fixture's
// projects adopt no way of working. It reads no repository head.
type noAdoptedChecks struct{ conformance.Source }

func (noAdoptedChecks) HistoricalChecksIn(context.Context, database.Queryer, []conformance.CheckVersion) ([]conformance.DeclaredCheck, error) {
	return nil, nil
}

func (noAdoptedChecks) ChecksIn(context.Context, database.Queryer, []ids.ID) (map[ids.ID][]conformance.DeclaredCheck, error) {
	return map[ids.ID][]conformance.DeclaredCheck{}, nil
}

func (f scannerOwnerFixture) bind(t *testing.T, name string, cfg map[string]any) {
	t.Helper()
	// A fixture scanner at a scan point applies to the repository unless
	// it says otherwise, as a scanner stored before appliesTo was migrated
	// to.
	if manual, _ := cfg["manual"].(bool); !manual {
		if _, ok := cfg["appliesTo"]; !ok {
			cfg["appliesTo"] = []string{"repository"}
		}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := integration.NewWithClock(f.DB, nil).Upsert(f.Context, name, ScannerCapability, "tool", string(b), true); err != nil {
		t.Fatal(err)
	}
}

// projectTree is a staged tree with a file in it and no contract: what a
// repository check applies to.
func projectTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"README.md": "an app\n"})
	return dir
}

func scannerSARIF(n int) string {
	var results []string
	for i := 0; i < n; i++ {
		results = append(results, `{"ruleId":"django.security.audit.sql-injection","level":"error","message":{"text":"Potential SQL injection"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"app/views.py"},"region":{"startLine":12}}}]}`)
	}
	return `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"semgrep","rules":[` +
		`{"id":"django.security.audit.sql-injection","defaultConfiguration":{"level":"error"},"properties":{"security-severity":"7.5"}}]}},` +
		`"results":[` + strings.Join(results, ",") + `]}]}`
}

func (f scannerOwnerFixture) semgrep(t *testing.T) {
	f.bind(t, "semgrep-django", map[string]any{
		"image": "semgrep/semgrep:test", "command": []string{"semgrep", "/work"},
		"report": ".apptown/semgrep.sarif", "network": true, "format": "sarif",
		"kind": "sast", "target": "source", "manual": true,
	})
}

// daemonExecutor is a check executor backed by a fake daemon: the same
// Docker commands the local executor would issue, so a test sees and
// answers them.
func daemonExecutor(scope docker.Scope) Executor {
	inspect := func(ref string) (execution.ImageAnswer, bool) {
		if _, err := scope.Run(context.Background(), "image", "inspect", ref); err != nil {
			return execution.ImageAnswer{}, false
		}
		return execution.ImageAnswer{ID: ref}, true
	}
	return Executor{
		Local: true,
		Image: func(_ context.Context, req execution.ImageRequest) execution.ImageAnswer {
			if req.Tag != "" {
				if a, ok := inspect(req.Tag); ok {
					return a
				}
			}
			switch {
			case req.Pull != "":
				if out, err := scope.Run(context.Background(), "pull", req.Pull); err != nil {
					return execution.ImageAnswer{Err: string(out)}
				}
				return execution.ImageAnswer{ID: req.Pull}
			case req.Context != "":
				if !req.AllowBuild {
					return execution.ImageAnswer{NotBuilt: true}
				}
				if out, err := scope.Run(context.Background(), "build", "-t", req.Tag, filepath.Join(req.Tree, req.Context)); err != nil {
					return execution.ImageAnswer{Err: fmt.Sprintf("building the app image: %v: %s", err, out)}
				}
				return execution.ImageAnswer{ID: req.Tag}
			}
			return execution.ImageAnswer{}
		},
		Run: func(_ context.Context, req execution.Request) execution.Outcome {
			args := []string{"run", "--rm", "--network", req.Network}
			for _, e := range req.Env {
				args = append(args, "-e", e)
			}
			out, err := scope.Run(context.Background(), append(append(args, req.Image), req.Argv...)...)
			var ee *exec.ExitError
			switch {
			case err == nil:
				return execution.Outcome{Termination: execution.Exited, Output: out}
			case errors.As(err, &ee):
				return execution.Outcome{Termination: execution.Exited, ExitCode: ee.ExitCode(), Output: out}
			}
			return execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: err, Output: out}
		},
	}
}
