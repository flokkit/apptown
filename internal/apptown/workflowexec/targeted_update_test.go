package workflowexec

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// The registry is the external-system seam. npm itself installs and updates
// real tarballs; neither the fix runner nor the classifier is replaced.
func npmRegistry(t *testing.T) (string, *atomic.Bool) {
	t.Helper()
	published := new(atomic.Bool)
	tarballs := map[string][]byte{}
	integrities := map[string]string{}
	for _, name := range []string{"example", "untargeted"} {
		for _, version := range []string{"1.0.0", "1.0.1"} {
			raw := []byte(fmt.Sprintf(`{"name":%q,"version":%q}`, name, version))
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0644, Size: int64(len(raw))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(raw); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			key := name + "-" + version
			tarballs[key] = buf.Bytes()
			sum := sha512.Sum512(buf.Bytes())
			integrities[key] = "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
		}
	}
	var url string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if strings.Contains(path, "/-/") {
			parts := strings.Split(path, "/-/")
			key := strings.TrimSuffix(parts[1], ".tgz")
			data, ok := tarballs[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(data)
			return
		}
		if path != "example" && path != "untargeted" {
			http.NotFound(w, r)
			return
		}
		versions := map[string]any{}
		latest := "1.0.0"
		for _, v := range []string{"1.0.0", "1.0.1"} {
			if v == "1.0.1" && !published.Load() {
				continue
			}
			latest = v
			versions[v] = map[string]any{"name": path, "version": v, "dist": map[string]string{"tarball": url + "/" + path + "/-/" + path + "-" + v + ".tgz", "integrity": integrities[path+"-"+v]}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"name": path, "dist-tags": map[string]string{"latest": latest}, "versions": versions})
	}))
	url = server.URL
	t.Cleanup(server.Close)
	return url, published
}

func fixtureShell(t *testing.T, folder string, argv []string, env []string) ([]byte, int) {
	t.Helper()
	out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Workspace: folder, Argv: argv, Env: env, Timeout: 30 * time.Second})
	if !out.Answered() {
		t.Fatalf("fixture command did not answer: %v %s", out.RunErr, out.Output)
	}
	return out.Output, out.ExitCode
}

type npmTaskDriver struct {
	*executionfake.Tasks
	mu    sync.Mutex
	specs map[string]execution.TaskSpec
	run   func(execution.TaskSpec) ([]byte, int)
}

func (d *npmTaskDriver) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	d.mu.Lock()
	given, ok := d.specs[spec.Key]
	if !ok {
		raw, exit := d.run(spec)
		given = spec
		given.Env = append(append([]string{}, spec.Env...), "FAKE_OUTPUT="+string(raw), fmt.Sprintf("FAKE_EXIT=%d", exit))
		d.specs[spec.Key] = given
	}
	d.mu.Unlock()
	return d.Tasks.Ensure(ctx, given)
}

func TestShippedTargetedUpdatePublishesOnlyItsTargetAndIsIndependentlyClassified(t *testing.T) {
	raw, err := stock.FS.ReadFile("janitor-fix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	callee, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	parent := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "scan", Steps: []schema.Step{
		{Name: "scan-source", Uses: "scan"},
		{Name: "fix", Uses: CallKind, With: map[string]any{"workflow": "janitor-fix", "whenFailed": []string{"scan-source"}, "findings": "fixable"}},
	}}}}
	f, listings := newNodeFixFixture(t, "scan", parent)
	f.definitions["janitor-fix"] = callee.Spec
	target := check.Finding{Fingerprint: "npm-vulnerability", Kind: "vuln", Severity: "high", Package: &check.PackageRef{Name: "example", Ecosystem: "npm", Version: "1.0.0", FixedIn: "1.0.1"}}
	f.scan = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		err := task.InAttempt("scan", func(tx *database.Tx, attempt int64) error {
			return conformance.RecordScanObservationIn(task.Context(), tx, conformance.ScanObservation{ProjectID: f.projectID, RunID: task.Run().ID, StepID: task.Step().ID, Attempt: attempt, Subject: conformance.ScanSubject{On: "source"}, Scanner: "osv", Verdict: "fail", ObservedAt: time.Now(), Findings: conformance.WrapFindings([]check.Finding{target})})
		})
		if err != nil {
			t.Error(err)
			return
		}
		task.Finish(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": "dependency vulnerability"}, nil))
	}
	run := f.startChangeRun(t, "scan", f.ctx)
	snapshot, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var scan, call ids.ID
	for _, s := range snapshot.Steps {
		if s.Name == "scan-source" {
			scan = s.ID
		} else {
			call = s.ID
		}
	}
	if err := f.work.ExecuteStep(f.ctx, scan); err != nil {
		t.Fatal(err)
	}
	if err := f.work.ExecuteStep(f.ctx, call); err != nil {
		t.Fatal(err)
	}
	snapshot, err = f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var child ids.ID
	for _, s := range snapshot.Steps {
		if s.ID == call {
			var ev struct {
				ChildRunID ids.ID          `json:"childRunId"`
				Targets    []check.Finding `json:"targets"`
			}
			if err := json.Unmarshal(s.Evidence, &ev); err != nil {
				t.Fatal(err)
			}
			child = ev.ChildRunID
			if len(ev.Targets) != 1 || ev.Targets[0].Package.Name != "example" {
				t.Fatalf("call targets: %s", s.Evidence)
			}
		}
	}
	if child.IsZero() {
		t.Fatal("fix did not admit a child")
	}
	snapshot, err = f.work.Snapshot(f.ctx, child, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var update, routine ids.ID
	for _, s := range snapshot.Steps {
		if s.Name == "update" {
			update = s.ID
		}
		if s.Name == "routine" {
			routine = s.ID
		}
	}
	if err := f.work.ExecuteStep(f.ctx, update); err != nil {
		t.Fatal(err)
	}
	if status, ev := stepStatus(t, f, update); status != workflow.StatusPass {
		t.Fatalf("targeted update %s: %+v", status, ev)
	}
	// The published Git object is independent of the command's own account.
	source, err := workflow.ReadSourceIn(t.Context(), f.db, workflow.RunRef{ID: child, ProjectID: f.projectID})
	if err != nil {
		t.Fatal(err)
	}
	branch := source.Branch
	locked := gittest.Run(t, f.hub, "show", branch+":package-lock.json")
	if !strings.Contains(locked, `"version": "1.0.1"`) {
		t.Fatalf("target did not move: %s", locked)
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		}
	}
	if err := json.Unmarshal([]byte(locked), &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Packages["node_modules/example"].Version != "1.0.1" || lock.Packages["node_modules/untargeted"].Version != "1.0.0" {
		t.Fatalf("targeted graph: %+v", lock.Packages)
	}
	if err := f.work.ExecuteStep(f.ctx, routine); err != nil {
		t.Fatal(err)
	}
	if status, ev := stepStatus(t, f, routine); status != workflow.StatusPass {
		t.Fatalf("independent classification %s: %+v", status, ev)
	}
	if listings.Load() != 2 {
		t.Fatalf("classifier observed %d trees", listings.Load())
	}
}

func newNodeFixFixture(t *testing.T, name string, spec schema.Spec) (*taskAppTown, *atomic.Int32) {
	t.Helper()
	registry, published := npmRegistry(t)
	f, _ := newTaskAppTown(t, name, spec)
	f.bound = "minor"
	f.ways.SyncStockGuidance(t.Context())
	node, err := f.ways.BlockByName(t.Context(), "lang/node/dependency-updates")
	if err != nil {
		t.Fatal(err)
	}
	receipt := adoption.EmptyReceipt()
	receipt.Guidance = append(receipt.Guidance, adoption.ReceiptBlock{Name: node.Name, BlockID: node.ID, Version: node.LatestVersion})
	if err := f.db.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.RecordIn(f.ctx, tx, f.projectID, 2, adoption.AdoptionEdit, "adopt node updates", receipt, taskAppTownAdministrator, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	// Use the tests step's mise fixture: these trees declare no pinned
	// toolchains, and npm/Go still execute real updates with the host tools.
	bin := t.TempDir()
	writeTestsMise(t, bin, "#!/bin/sh\nexit 0\n")
	config := t.TempDir()
	for _, name := range []string{"user.npmrc", "global.npmrc", "global-mise.toml", "system-mise.toml"} {
		if err := os.WriteFile(filepath.Join(config, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"npm_config_registry=" + registry, "npm_config_audit=false", "npm_config_fund=false", "npm_config_prefer_online=true", "npm_config_cache=" + t.TempDir(), "PATH=" + bin + ":" + os.Getenv("PATH"), "MISE_DATA_DIR=" + t.TempDir(), "MISE_CACHE_DIR=" + t.TempDir(), "MISE_CONFIG_DIR=" + t.TempDir(), "MISE_STATE_DIR=" + t.TempDir(), "MISE_GLOBAL_CONFIG_FILE=" + filepath.Join(config, "global-mise.toml"), "MISE_SYSTEM_CONFIG_FILE=" + filepath.Join(config, "system-mise.toml"), "npm_config_userconfig=" + filepath.Join(config, "user.npmrc"), "npm_config_globalconfig=" + filepath.Join(config, "global.npmrc")}
	if err := os.MkdirAll(filepath.Join(f.hub, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"package.json": `{"name":"app","version":"1.0.0","dependencies":{"example":"^1.0.0","untargeted":"^1.0.0"}}`, ".apptown/app.yaml": "app:\n  web:\n    build: .\n    public: true\n    tools: [node]\n"} {
		if err := os.WriteFile(filepath.Join(f.hub, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if out, code := fixtureShell(t, f.hub, []string{"npm", "install", "--package-lock-only", "--ignore-scripts"}, env); code != 0 {
		t.Fatalf("initial lock: %d %s", code, out)
	}
	gittest.Run(t, f.hub, "add", ".")
	gittest.Run(t, f.hub, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "dependencies")
	published.Store(true)
	markers := t.TempDir()
	driver := &npmTaskDriver{Tasks: &executionfake.Tasks{}, specs: map[string]execution.TaskSpec{}, run: func(spec execution.TaskSpec) ([]byte, int) {
		if spec.Image != f.commands.checks.Image {
			t.Errorf("fix image %q differs from tests image %q", spec.Image, f.commands.checks.Image)
		}
		argv := append([]string{}, spec.Argv...)
		for i := range argv {
			argv[i] = strings.ReplaceAll(argv[i], "/tmp/.forge-task-exit", filepath.Join(markers, spec.Key))
		}
		return fixtureShell(t, spec.Workspace, argv, append(append([]string{}, spec.Env...), env...))
	}}
	f.tasks.Driver = driver
	listings := new(atomic.Int32)
	f.checks = func(req execution.Request) execution.Outcome {
		if req.Report == ".apptown-update-list.jsonl" {
			listings.Add(1)
		}
		argv := append([]string{}, req.Argv...)
		for i := range argv {
			argv[i] = strings.ReplaceAll(argv[i], "/work/", filepath.ToSlash(req.Workspace)+"/")
		}
		mappedEnv := append([]string{}, req.Env...)
		for i := range mappedEnv {
			mappedEnv[i] = strings.ReplaceAll(mappedEnv[i], "/work/", filepath.ToSlash(req.Workspace)+"/")
		}
		out, exit := fixtureShell(t, req.Workspace, argv, append(mappedEnv, env...))
		report, err := os.ReadFile(filepath.Join(req.Workspace, req.Report))
		if req.Report != ".apptown-update-list.jsonl" && os.IsNotExist(err) {
			err = nil
		}
		return execution.Outcome{Termination: execution.Exited, ExitCode: exit, Output: out, Report: report, ReportErr: err}
	}
	return f, listings
}

func TestFixUsesWholeDeclarationsAndSkipsPartsWithoutTargets(t *testing.T) {
	for _, targeted := range []bool{false, true} {
		t.Run(fmt.Sprintf("targeted=%v", targeted), func(t *testing.T) {
			with := map[string]any{"kind": "update"}
			if targeted {
				with["for"] = "findings"
			}
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "fix", Steps: []schema.Step{{Name: "update", Uses: FixKind, With: with}}}}}
			f, _ := newNodeFixFixture(t, "multi", spec)
			goBlock, err := f.ways.BlockByName(t.Context(), "lang/go/dependency-updates")
			if err != nil {
				t.Fatal(err)
			}
			nodeBlock, err := f.ways.BlockByName(t.Context(), "lang/node/dependency-updates")
			if err != nil {
				t.Fatal(err)
			}
			receipt := adoption.EmptyReceipt()
			receipt.Guidance = []adoption.ReceiptBlock{{Name: nodeBlock.Name, BlockID: nodeBlock.ID, Version: nodeBlock.LatestVersion}, {Name: goBlock.Name, BlockID: goBlock.ID, Version: goBlock.LatestVersion}}
			if err := f.db.InTx(t.Context(), func(tx *database.Tx) error {
				return adoption.RecordIn(f.ctx, tx, f.projectID, 3, adoption.AdoptionEdit, "adopt updates", receipt, taskAppTownAdministrator, time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(f.hub, "service"), 0755); err != nil {
				t.Fatal(err)
			}
			for path, body := range map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    public: true\n    tools: [node]\n  service:\n    build: service\n    tools: [go]\n", "service/go.mod": "module example.test/service\n\ngo 1.23\n", "service/main.go": "package main\nfunc main() {}\n"} {
				if err := os.WriteFile(filepath.Join(f.hub, path), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			gittest.Run(t, f.hub, "add", ".")
			gittest.Run(t, f.hub, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qm", "two languages")
			driver := f.tasks.Driver.(*npmTaskDriver)
			runCommand := driver.run
			var command string
			driver.run = func(s execution.TaskSpec) ([]byte, int) { command = strings.Join(s.Argv, " "); return runCommand(s) }
			ch := f.beginChange(t)
			f.work.Stop()
			inputs := `{}`
			if targeted {
				raw, _ := json.Marshal([]check.Finding{{Fingerprint: "target", Package: &check.PackageRef{Name: "example", Ecosystem: "npm", FixedIn: "1.0.1"}}})
				encoded, _ := json.Marshal(map[string]string{"targets": string(raw)})
				inputs = string(encoded)
			}
			run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "multi", Inputs: json.RawMessage(inputs)})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			step := queueSteps(t, f, run)["update"]
			if err := f.work.ExecuteStep(f.ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			if state, ev := stepStatus(t, f, step.ID); state != workflow.StatusPass {
				t.Fatalf("%s: %+v", state, ev)
			}
			if strings.Contains(command, "go get -u=patch ./...") == targeted {
				t.Fatalf("wrong part selection: %s", command)
			}
			if targeted && !strings.Contains(command, "npm update 'example'") {
				t.Fatalf("target lost: %s", command)
			}
		})
	}
}

func TestUncoveredToolErrorsWithItsPart(t *testing.T) {
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "fix", Steps: []schema.Step{{Name: "update", Uses: FixKind, With: map[string]any{"kind": "update"}}}}}}
	f, _ := newNodeFixFixture(t, "uncovered", spec)
	if err := os.WriteFile(filepath.Join(f.hub, ".apptown/app.yaml"), []byte("app:\n  server:\n    build: .\n    public: true\n    tools: [java]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, f.hub, "add", ".")
	gittest.Run(t, f.hub, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qm", "uncovered tool")
	run := f.startChangeRun(t, "uncovered", f.ctx)
	step := queueSteps(t, f, run)["update"]
	if err := f.work.ExecuteStep(f.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	state, ev := stepStatus(t, f, step.ID)
	if state != workflow.StatusError || !strings.Contains(fmt.Sprint(ev), "java") || !strings.Contains(fmt.Sprint(ev), "server") {
		t.Fatalf("%s: %+v", state, ev)
	}
}

func TestRoutineJudgesTheIndependentListingAndLeavesCodeToChecks(t *testing.T) {
	for _, tc := range []struct {
		name         string
		update       bool
		bound, fixed string
		want         workflow.StepState
		reason       string
	}{
		{"code beside update", true, "minor", "1.0.1", workflow.StatusPass, ""},
		{"target below FixedIn", true, "minor", "2.0.0", workflow.StatusFail, "example"},
		{"empty candidate", false, "minor", "", workflow.StatusNotApplicable, "nothing to change"},
		{"no eligible rule", false, "", "", workflow.StatusNotApplicable, "no eligible update rule"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "fix", Steps: []schema.Step{{Name: "update", Uses: FixKind, With: map[string]any{"kind": "update"}}, {Name: "routine", Uses: RoutineKind}}}}}
			f, _ := newNodeFixFixture(t, "routine", spec)
			f.bound = tc.bound
			ch := f.beginChange(t)
			f.work.Stop()
			raw, _ := json.Marshal([]check.Finding{{Fingerprint: "fixed", Package: &check.PackageRef{Name: "example", Ecosystem: "npm", FixedIn: tc.fixed}}})
			inputs, _ := json.Marshal(map[string]string{"targets": string(raw)})
			run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "routine", Inputs: inputs})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			steps := queueSteps(t, f, run)
			if tc.update {
				driver := f.tasks.Driver.(*npmTaskDriver)
				execute := driver.run
				driver.run = func(s execution.TaskSpec) ([]byte, int) {
					out, exit := execute(s)
					if exit == 0 {
						if err := os.WriteFile(filepath.Join(s.Workspace, "repair.js"), []byte("console.log('a code repair beside the update')\n"), 0644); err != nil {
							t.Error(err)
						}
					}
					return out, exit
				}
				if err := f.work.ExecuteStep(f.ctx, steps["update"].ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.work.ExecuteStep(f.ctx, steps["routine"].ID); err != nil {
				t.Fatal(err)
			}
			state, ev := stepStatus(t, f, steps["routine"].ID)
			if state != tc.want || tc.reason != "" && !strings.Contains(fmt.Sprint(ev), tc.reason) {
				t.Fatalf("%s: %+v", state, ev)
			}
		})
	}
}

// The former npm assess fixture families now go through the shipped node list
// command, npm ci and npm ls, then the generic classifier. Code and declaration
// range edits are allowed here; the app's checks judge them.
func TestNodeAssessFixturesUseTheShippedListing(t *testing.T) {
	for _, tc := range []struct {
		name                string
		edit                func(string) string
		manifest, competing bool
		want                workflow.StepState
	}{
		{"unknown metadata", func(s string) string {
			return strings.Replace(s, `"version": "1.0.1"`, `"unexplained": 42, "version": "1.0.1"`, 1)
		}, false, false, workflow.StatusFail},
		{"foreign source", func(s string) string { return strings.ReplaceAll(s, "127.0.0.1", "localhost") }, false, false, workflow.StatusFail},
		{"source userinfo", func(s string) string { return strings.ReplaceAll(s, "http://", "http://secret@") }, false, false, workflow.StatusError},
		{"duplicate nested key", func(s string) string {
			return strings.Replace(s, `"version": "1.0.1"`, `"version": "1.0.0", "version": "1.0.1"`, 1)
		}, false, false, workflow.StatusError},
		{"install script", func(s string) string {
			return strings.Replace(s, `"version": "1.0.1"`, `"hasInstallScript": true, "version": "1.0.1"`, 1)
		}, false, false, workflow.StatusFail},
		{"optional", func(s string) string {
			return strings.Replace(s, `"version": "1.0.1"`, `"optional": true, "version": "1.0.1"`, 1)
		}, false, false, workflow.StatusFail},
		{"link", func(s string) string {
			return strings.Replace(s, `"version": "1.0.1"`, `"link": true, "version": "1.0.1"`, 1)
		}, false, false, workflow.StatusError},
		{"new dependency", func(s string) string {
			return strings.Replace(s, `"dependencies":`, `"dependencies":{"missing":"1.0.0",`, 1)
		}, true, false, workflow.StatusError},
		{"script", func(s string) string {
			return strings.Replace(s, `"dependencies":`, `"scripts":{"test":"node --test"},"dependencies":`, 1)
		}, true, false, workflow.StatusPass},
		{"range operator", func(s string) string { return strings.ReplaceAll(s, "^1.0.0", "~1.0.0") }, true, false, workflow.StatusPass},
		{"competing lock", func(s string) string { return s }, false, true, workflow.StatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "fix", Steps: []schema.Step{{Name: "update", Uses: FixKind, With: map[string]any{"kind": "update"}}, {Name: "routine", Uses: RoutineKind}}}}}
			f, _ := newNodeFixFixture(t, "assess", spec)
			driver := f.tasks.Driver.(*npmTaskDriver)
			execute := driver.run
			driver.run = func(s execution.TaskSpec) ([]byte, int) {
				out, exit := execute(s)
				if exit != 0 {
					return out, exit
				}
				name := "package-lock.json"
				if tc.manifest {
					name = "package.json"
				}
				path := filepath.Join(s.Workspace, name)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Error(err)
					return out, 1
				}
				if err := os.WriteFile(path, []byte(tc.edit(string(raw))), 0644); err != nil {
					t.Error(err)
					return out, 1
				}
				if tc.competing {
					if err := os.WriteFile(filepath.Join(s.Workspace, "npm-shrinkwrap.json"), raw, 0644); err != nil {
						t.Error(err)
					}
				}
				return out, exit
			}
			run := f.startChangeRun(t, "assess", f.ctx)
			steps := queueSteps(t, f, run)
			for _, name := range []string{"update", "routine"} {
				if err := f.work.ExecuteStep(f.ctx, steps[name].ID); err != nil {
					t.Fatal(err)
				}
			}
			if state, ev := stepStatus(t, f, steps["routine"].ID); state != tc.want {
				t.Fatalf("%s: %+v", state, ev)
			}
		})
	}
}
