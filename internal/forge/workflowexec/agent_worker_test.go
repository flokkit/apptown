package workflowexec

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/agentenv"
	piharness "flokkit.io/forge/internal/forge/agentharness/pi"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/progress"
)

// Values the forge holds that no worker may be given. Each is placed where
// the forge keeps it (the instance's secrets file, its own environment); a
// request that carried one anywhere would show it.
const (
	engineSecretCanary   = "engine-secret-canary"   // GITLAB_TOKEN: an engine secret
	providerKeyCanary    = "provider-key-canary"    // a model provider's key
	databaseURLCanary    = "database-url-canary"    // the forge's database
	instanceCredCanary   = "instance-cred-canary"   // the forge's own admin token
	stepSecretCanary     = "step-secret-canary"     // STEP_CANARY: delivered, by design
	evilFolderSteering   = "folder-steering-canary" // what a .forge link in the folder would offer
	workerSubagents      = "worker-subagents-1.2"
	hostSubagents        = "host-subagents-9.9"
	workerAgentImageName = "forge-agent-worker:sha256-abc"
)

// agentWorkerDriver is a worker whose only permitted call is its agent
// image; any other call fails the test (nothing is sent while a request is
// built). imageAllowed false fails that too: a refusal from what was
// admitted asks the worker nothing.
type agentWorkerDriver struct {
	t            *testing.T
	image        execution.AgentImage
	imageAllowed bool
	imageReads   int
	// reported is the profile it reports; nil is workerProfile.
	reported *execution.WorkerProfile
}

func (d *agentWorkerDriver) effect(what string) error {
	d.t.Errorf("the worker was asked to %s while a request was built", what)
	return errors.New("no effects here")
}
func (d *agentWorkerDriver) Capabilities() execution.Capabilities {
	return execution.Capabilities{Inputs: true, Results: true}
}
func (d *agentWorkerDriver) Ensure(context.Context, execution.TaskSpec) (execution.TaskObservation, error) {
	return execution.TaskObservation{}, d.effect("ensure a task")
}
func (d *agentWorkerDriver) Start(context.Context, execution.TaskRef) error {
	return d.effect("start a task")
}
func (d *agentWorkerDriver) Observe(context.Context, execution.TaskRef) (execution.TaskObservation, error) {
	return execution.TaskObservation{}, d.effect("observe a task")
}
func (d *agentWorkerDriver) Wait(context.Context, execution.TaskRef) (int, error) {
	return 0, d.effect("wait for a task")
}
func (d *agentWorkerDriver) Stop(context.Context, execution.TaskRef) error {
	return d.effect("stop a task")
}
func (d *agentWorkerDriver) Output(context.Context, execution.TaskRef, execution.OutputRead) (execution.OutputPage, error) {
	return execution.OutputPage{}, d.effect("read output")
}
func (d *agentWorkerDriver) Release(context.Context, execution.TaskRef) error {
	return d.effect("release a task")
}
func (d *agentWorkerDriver) ReleaseWorkspace(context.Context, string) error {
	return d.effect("release a workspace")
}
func (d *agentWorkerDriver) Results(context.Context, execution.TaskRef) (map[string]execution.ResultFile, error) {
	return nil, d.effect("read result files")
}
func (d *agentWorkerDriver) Profile(context.Context) (execution.WorkerProfile, error) {
	if !d.imageAllowed {
		return execution.WorkerProfile{}, d.effect("report its profile")
	}
	if d.reported != nil {
		return *d.reported, nil
	}
	return workerProfile, nil
}

// workerProfile is what the fake worker reports it measured.
var workerProfile = execution.WorkerProfile{Name: execution.ProfileWorkerVM, OS: "linux Debian GNU/Linux 13 (trixie)", Architecture: "x86_64", Kernel: "6.8.12",
	Cgroup: "v2 (systemd)", Docker: "26.1.5", StorageDriver: "overlay2", DindImage: "docker:27-dind", DindImageID: "sha256:d1d",
	DindDigest: "docker@sha256:aa3", DindVersion: "27.5.1, build 4c9b3b0", Privileged: "yes", Exclusive: true, Egress: "not contained"}

func (d *agentWorkerDriver) AgentImage(context.Context) (execution.AgentImage, error) {
	d.imageReads++
	if !d.imageAllowed {
		return execution.AgentImage{}, d.effect("report its agent image")
	}
	return d.image, nil
}

// agentCatalog holds two blocks: pinned guidance (so the steering carries
// its hygiene and attachment sections) and a resource.
type agentCatalog struct{}

func (agentCatalog) Block(ctx context.Context, ref wow.BlockRef) (wow.BlockObservation, error) {
	switch {
	case ref.Name == "rules/plain" || ref.ID == idstest.N(7):
		return wow.BlockObservation{Block: wow.Block{ID: idstest.N(7), Name: "rules/plain", LatestVersion: 1}, Version: 1, Body: "Keep it plain."}, nil
	case ref.Name == "res/notes" || ref.ID == idstest.N(8):
		return wow.BlockObservation{Block: wow.Block{ID: idstest.N(8), Name: "res/notes", LatestVersion: 1}, Version: 1, Body: "resource body"}, nil
	}
	return wow.BlockObservation{}, sql.ErrNoRows
}
func (agentCatalog) Workflow(context.Context, wow.WorkflowRef) (wow.WorkflowObservation, error) {
	return wow.WorkflowObservation{}, sql.ErrNoRows
}
func (agentCatalog) Workflows(ctx context.Context) ([]wow.WorkflowEntry, error) { return nil, nil }
func (agentCatalog) Withdrawn(wow.Kind, string, string) bool                    { return false }

func (agentCatalog) PackPublication(context.Context, string, int64) (wow.PackVersion, error) {
	return wow.PackVersion{}, sql.ErrNoRows
}

type gatewayModels struct{}

func (gatewayModels) ModelsJSON(context.Context) ([]byte, error) {
	return []byte(`{"providers":{"forge":{"baseUrl":"http://gateway.test/v1","apiKey":"FORGE_SESSION_TOKEN"}}}`), nil
}

type agentExtensions []string

func (e agentExtensions) EnabledExtensions(context.Context, ids.ID) ([]string, error) { return e, nil }

type fileCLI string

func (c fileCLI) Path() (string, error) { return string(c), nil }

type agentOnWorker struct {
	exec   *Executor
	worker *agentWorkerDriver
	call   workerAgentCall
	dir    string
	hub    string
}

// agentOnWorkerFixture is an admitted agent step of a repository-backed
// change on a forge with a worker, built through the owners that hold what
// the request reads: the project and its adoption, the instance's secrets,
// the attachment store and the project's hub. The run's folder is never
// made. extensions are the project's enabled extensions.
func agentOnWorkerFixture(t *testing.T, config map[string]any, extensions ...string) *agentOnWorker {
	t.Helper()
	return agentOnWorkerFixtureFor(t, project.Ordinary, config, extensions...)
}

// agentOnWorkerFixtureFor is the fixture in a project made with purpose.
func agentOnWorkerFixtureFor(t *testing.T, purpose project.Purpose, config map[string]any, extensions ...string) *agentOnWorker {
	t.Helper()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	t.Setenv("DATABASE_URL", "postgres://forge:"+databaseURLCanary+"@db/forge")
	t.Setenv("FORGE_ADMIN_TOKEN", instanceCredCanary)
	t.Setenv("ANTHROPIC_API_KEY", providerKeyCanary)
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("STEP_CANARY="+stepSecretCanary+"\nGITLAB_TOKEN="+engineSecretCanary+"\nANTHROPIC_API_KEY="+providerKeyCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt := adoption.EmptyReceipt()
	receipt.Guidance = []adoption.ReceiptBlock{{Name: "rules/plain", BlockID: idstest.N(7), Version: 1}}
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		id, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "agents", Name: "Agents", Purpose: purpose})
		if err != nil {
			return ids.Nil, err
		}
		if err := adoption.InitializeIn(t.Context(), tx, id, 0, "", ""); err != nil {
			return ids.Nil, err
		}
		return id, adoption.RecordIn(t.Context(), tx, id, 1, adoption.AdoptionCreate, "agent fixture", receipt, "tester", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	adoptions := adoption.New(db, adoption.Dependencies{Ways: agentCatalog{}})
	current, err := adoptions.CurrentReceipt(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := integration.New(db).Upsert(t.Context(), "npm-mirror", "registry", "nexus", `{"format":"npm","url":"http://mirror.test/npm"}`, false); err != nil {
		t.Fatal(err)
	}

	// The change's attachment, in the store the upload path writes.
	changeID := idstest.N(41)
	attachments := (workflow.GitWorkspace{DataDir: dir}).AttachmentDir(changeID)
	if err := os.MkdirAll(attachments, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachments, "spec.txt"), []byte("attached spec"), 0o644); err != nil {
		t.Fatal(err)
	}

	source := workflow.Source{RunID: idstest.N(5), ProjectID: projectID, RootRunID: idstest.N(5), ChangeID: changeID, Slug: "agents", DefaultBranch: "main", Branch: "change-00000000-0029-7000-8000-000000000000", WorkflowName: "build", WorkflowVersion: 1}
	hub := source.Repository(dir)
	commit := hubCommit(t, hub, map[string]string{"README.md": "hello\n"})

	cli := filepath.Join(dir, "forge-linux")
	if err := execfile.Write(cli, []byte("#!/bin/sh\necho packaged forge CLI\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tokens := &identity.RuntimeTokens{}
	if err := tokens.Open(filepath.Join(dir, identity.RuntimeTokenKeyFile)); err != nil {
		t.Fatal(err)
	}
	// The forge's own Docker has an agent image with other labels than the
	// worker's: a request built from them would promise what the worker's
	// image does not carry.
	host := dockerfake.New()
	host.SetReply(func(args []string) ([]byte, error, bool) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, piharness.SubagentsLabel):
			return []byte(hostSubagents), nil, true
		case strings.Contains(joined, "forge.pi-lsp.version"):
			return []byte("host-lsp-1"), nil, true
		}
		return nil, nil, true
	})
	scope := &docker.Scope{Client: host}
	integrations := integration.NewService(db, integration.Dependencies{Projects: project.New(db, nil)})
	catalog := dependency.NewCatalog(db, nil)
	if _, err := catalog.Save(context.Background(), postgresKind("16"), "tester"); err != nil {
		t.Fatal(err)
	}
	worker := &agentWorkerDriver{t: t, imageAllowed: true, image: execution.AgentImage{Name: workerAgentImageName,
		Labels: map[string]string{piharness.SubagentsLabel: workerSubagents, "forge.pi.version": "0.9"}}}
	e := New(db, Dependencies{
		Adoption:     adoptions,
		Integrations: integrations,
		Catalog:      catalog,
		Instructions: agentenv.NewInstructions(db, integrations),
		Secrets:      secret.NewService(db, secret.Config{DataDir: dir}),
		Models:       model.NewSelection(model.New(db), "fake/model"),
		Capacity:     model.NewScheduler(nil, 1),
		Tokens:       tokens,
		Progress:     &progress.Feeds{},
		Docker:       scope,
		Home:         piharness.NewPreparer(gatewayModels{}, model.NewSelection(model.New(db), "fake/model"), agentExtensions(extensions), scope, "forge-agent"),
		CLI:          fileCLI(cli),
		Ledger:       usage.New(db),
		Worker:       &AgentWorker{Tasks: &TaskRunner{Driver: worker, Profile: TaskProfile{Name: "worker", Network: execution.NetworkDefault}}},
	}, Config{DataDir: dir, Image: "forge-agent", Backend: execution.Docker, APIURL: "http://forge.test:7700", PrivateDockerOnWorker: true})

	cfg := map[string]any{"prompt": "Implement the change.", "resources": []string{"res/notes"}, "output": map[string]any{"file": ".forge/REPORT.md"}, "timeoutMinutes": 20}
	for k, v := range config {
		cfg[k] = v
	}
	stepConfig, _ := json.Marshal(cfg)
	definition, _ := json.Marshal(schema.Spec{Kind: "pipeline", Role: "working"})
	run := &workflow.Run{ID: idstest.N(5), ProjectID: projectID, WorkflowName: "build", WorkflowVersion: 1, ChangeID: changeID, AdoptionID: current.ID, DefinitionJSON: string(definition)}
	step := &workflow.Step{ID: idstest.N(9), RunID: idstest.N(5), Attempt: 2, Stage: "work", Name: "implement", Type: AgentKind, Config: stepConfig}
	return &agentOnWorker{exec: e, worker: worker, dir: dir, hub: hub, call: workerAgentCall{run: run, step: step, source: source, commit: commit,
		invocation: "agent", at: time.Date(2026, 10, 1, 9, 30, 15, 250_000_000, time.UTC), session: "step-00000000-0009-7000-8000-000000000000-a2"}}
}

// hubCommit commits files to the project's hub (made on first use) and
// returns the commit.
func hubCommit(t *testing.T, hub string, files map[string]string) string {
	t.Helper()
	git := func(args ...string) string {
		cmd := gittest.Command(args...)
		cmd.Dir = hub
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if _, err := os.Stat(filepath.Join(hub, ".git")); err != nil {
		if err := os.MkdirAll(hub, 0o755); err != nil {
			t.Fatal(err)
		}
		git("init", "-q", "-b", "main")
		gittest.ConfigureTester(t, hub)
	}
	for name, body := range files {
		at := filepath.Join(hub, name)
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(body, "link:") {
			_ = os.Remove(at)
			if err := os.Symlink(strings.TrimPrefix(body, "link:"), at); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(at, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "fixture")
	return git("rev-parse", "HEAD")
}

// inputContent is what an input presents: its blob's one entry.
func inputContent(t *testing.T, in execution.Input) []byte {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(in.BlobTar()))
	if _, err := tr.Next(); err != nil {
		t.Fatalf("input %s: %v", in.Path, err)
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func inputsByPath(t *testing.T, spec execution.TaskSpec) map[string]execution.Input {
	t.Helper()
	out := map[string]execution.Input{}
	for _, in := range spec.Inputs {
		out[in.Path] = in
	}
	return out
}

func (f *agentOnWorker) build(t *testing.T) workerAgentRequest {
	t.Helper()
	req, err := f.exec.prepareAgentOnWorker(context.Background(), f.call)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// The request an agent step's invocation sends a worker is built as bytes
// from what was admitted: the files the agent reads at their /forge/in
// paths, the transcript the forge named, the result files it declares, and
// only the runtime token, step secrets and mirror settings in its
// environment. The run's folder is never made, read or written.
func TestAnAgentStepOnAWorkerIsSentWhatWasAdmitted(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	req := f.build(t)
	spec := req.spec
	folder := f.call.source.Workspace(f.dir)

	if spec.Key != "step-00000000-0009-7000-8000-000000000000-attempt-2-agent" || spec.SharedWorkspace != "step-00000000-0009-7000-8000-000000000000-a2" || spec.Image != workerAgentImageName {
		t.Fatalf("identity: key %q workspace %q image %q", spec.Key, spec.SharedWorkspace, spec.Image)
	}
	if spec.Source == nil || spec.Source.Repository != f.hub || spec.Source.Commit != f.call.commit {
		t.Fatalf("source: %+v", spec.Source)
	}
	// The agent's own repository is on the change's branch; the base branch
	// is named only at a change base in the source's history.
	if spec.AgentGit == nil || *spec.AgentGit != (execution.AgentGit{Branch: "change-00000000-0029-7000-8000-000000000000"}) {
		t.Fatalf("the agent's repository without a change base: %+v", spec.AgentGit)
	}
	for base, want := range map[string]execution.AgentGit{
		f.call.commit:           {Branch: "change-00000000-0029-7000-8000-000000000000", Base: "main", BaseCommit: f.call.commit},
		strings.Repeat("0", 40): {Branch: "change-00000000-0029-7000-8000-000000000000"},
	} {
		source := f.call.source
		source.ChangeBaseSHA = base
		if got := agentGit(context.Background(), source, f.call.commit, f.hub); *got != want {
			t.Fatalf("the agent's repository with the change base %s: %+v", base, got)
		}
	}
	if spec.Workspace != "" || len(spec.Mounts) > 0 || len(spec.ExtraHosts) > 0 {
		t.Fatalf("a worker's request names no forge host path: %+v", spec)
	}
	adm := spec.Admission
	if adm.Profile != "worker" || adm.Network != execution.NetworkDefault || adm.Limits.WallClock != 20*time.Minute || !adm.Expires.Equal(f.call.at.Add(20*time.Minute+taskGrace)) {
		t.Fatalf("admission: %+v", adm)
	}

	session := piharness.SessionFileName("step-00000000-0009-7000-8000-000000000000-a2", f.call.at)
	if session != "2026-10-01T09-30-15-250Z_step-00000000-0009-7000-8000-000000000000-a2.jsonl" || req.sessionFile != session {
		t.Fatalf("the transcript's name: %q (%q)", session, req.sessionFile)
	}
	inputs := inputsByPath(t, spec)
	want := map[string]os.FileMode{
		"/forge/in/steering.md":                        0o644,
		"/forge/in/resources/res--notes.md":            0o644,
		"/forge/in/attachments/spec.txt":               0o644,
		"/forge/in/pi-home/.pi/agent/models.json":      0o644,
		"/forge/in/pi-home/.pi/agent/settings.json":    0o644,
		"/forge/in/pi-home/.pi/agent/agents/worker.md": 0o644,
		"/forge/in/pi-home/.pi/agent/agents/scout.md":  0o644,
		"/forge/in/bin/forge":                          0o755,
		"/forge/in/sessions/" + session:                0o644,
	}
	for at, mode := range want {
		if in, ok := inputs[at]; !ok || in.Mode != mode {
			t.Errorf("input %s: %+v (want mode %v)", at, in, mode)
		}
	}
	for at := range inputs {
		if _, ok := want[at]; !ok {
			t.Errorf("an input nobody asked for: %s", at)
		}
	}
	if got := string(inputContent(t, inputs["/forge/in/bin/forge"])); !strings.Contains(got, "packaged forge CLI") {
		t.Errorf("the CLI: %q", got)
	}
	if got := string(inputContent(t, inputs["/forge/in/attachments/spec.txt"])); got != "attached spec" {
		t.Errorf("the attachment: %q", got)
	}
	if got := string(inputContent(t, inputs["/forge/in/resources/res--notes.md"])); got != "resource body" {
		t.Errorf("the resource: %q", got)
	}
	header := inputContent(t, inputs["/forge/in/sessions/"+session])
	if id, err := piharness.SessionHeaderID(header); err != nil || id != "step-00000000-0009-7000-8000-000000000000-a2" || bytes.Count(header, []byte("\n")) != 1 || !bytes.Contains(header, []byte(`"cwd":"/work"`)) {
		t.Errorf("a new session is sent its header only: %q %v", header, err)
	}

	if len(spec.Results) != 2 ||
		spec.Results[0] != (execution.Result{Name: "transcript", Path: "/forge/state/sessions/" + session, Max: 32 << 20}) ||
		spec.Results[1] != (execution.Result{Name: "output", Path: "/work/.forge/REPORT.md", Max: 1 << 20}) {
		t.Errorf("declared results: %+v", spec.Results)
	}

	// Environment: step secrets, the attempt's runtime token, the forge's
	// address and the one ref the token may push (for `forge check`), the
	// mirror settings. Nothing else.
	var names []string
	for _, kv := range spec.Env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	slices.Sort(names)
	amb := agentenv.AmbientFor(map[string]integration.BoundRegistry{"npm": {Name: "npm-mirror", URL: "http://mirror.test/npm"}}, "step-00000000-0009-7000-8000-000000000000-a2")
	var ambNames []string
	for _, kv := range amb.Env {
		n, _, _ := strings.Cut(kv, "=")
		ambNames = append(ambNames, n)
	}
	wantNames := append([]string{"CANARY", "FORGE_CHANGE", "FORGE_PROJECT", "FORGE_RUN_ID", "FORGE_SCRATCH_REF", "FORGE_SESSION_TOKEN", "FORGE_STEP", "FORGE_URL",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"}, ambNames...)
	slices.Sort(wantNames)
	if !slices.Equal(names, wantNames) {
		t.Errorf("env names:\n got %v\nwant %v", names, wantNames)
	}
	if !slices.Contains(spec.Env, "CANARY="+stepSecretCanary) || !slices.Contains(spec.Env, "FORGE_SESSION_TOKEN="+f.exec.runTok.Step(idstest.N(9), 2)) {
		t.Errorf("the step secret and the attempt's token are delivered: %v", names)
	}
	if !slices.Contains(spec.Env, "FORGE_SCRATCH_REF="+scm.ScratchRef(f.call.source.ChangeID, idstest.N(9), 2)) {
		t.Errorf("the attempt's own scratch ref is delivered: %v", spec.Env)
	}
	if len(spec.Files) == 0 || !slices.ContainsFunc(spec.Files, func(file execution.File) bool { return file.Path == "/etc/mise/config.toml" }) {
		t.Errorf("the mirror's files: %+v", spec.Files)
	}

	// argv: the entry, then pi with its state outside the tree.
	if len(spec.Argv) < 5 || spec.Argv[0] != "sh" || spec.Argv[1] != "-c" || spec.Argv[3] != "sh" {
		t.Fatalf("argv: %q", spec.Argv)
	}
	entry := spec.Argv[2]
	for _, line := range []string{
		"cp -R /forge/in/pi-home /forge/state/home.part",
		"ln -s '/opt/pi-baked/.pi/agent/npm' '/forge/state/home.part/.pi/agent/npm'",
		"mv /forge/state/home.part /forge/state/home",
		"if [ ! -e '/forge/state/sessions/" + session + "' ]; then",
		"cp '/forge/in/sessions/" + session + "' /forge/state/session.part",
		"export HOME=/forge/state/home",
		`export PATH="/forge/in/bin:$PATH"`,
		`exec "$@"`,
	} {
		if !strings.Contains(entry, line) {
			t.Errorf("the entry lacks %q:\n%s", line, entry)
		}
	}
	command := strings.Join(spec.Argv[4:], "\x00")
	for _, arg := range []string{"pi", "--session\x00/forge/state/sessions/" + session, "--append-system-prompt\x00/forge/in/steering.md", "Implement the change."} {
		if !strings.Contains(command, arg) {
			t.Errorf("pi's argv lacks %q: %q", arg, spec.Argv[4:])
		}
	}
	if strings.Contains(command, "--session-id") || strings.Contains(command, "--session-dir") {
		t.Errorf("pi is left to find its session by id: %q", spec.Argv[4:])
	}
	if !strings.Contains(command, console.ToolchainPrelude) {
		t.Errorf("a bound mirror installs the declared toolchain first: %q", spec.Argv[4:])
	}

	// Nothing the forge holds for itself is in anything sent.
	var sent []string
	sent = append(sent, spec.Env...)
	sent = append(sent, spec.Argv...)
	for _, in := range spec.Inputs {
		sent = append(sent, string(inputContent(t, in)))
	}
	for _, file := range spec.Files {
		sent = append(sent, string(file.Content))
	}
	for _, s := range sent {
		for _, canary := range []string{engineSecretCanary, providerKeyCanary, databaseURLCanary, instanceCredCanary} {
			if strings.Contains(s, canary) {
				t.Errorf("the request carries %s", canary)
			}
		}
	}
	if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the run's folder was made: %v", err)
	}
	if f.worker.imageReads != 1 {
		t.Errorf("the worker's image was read %d times", f.worker.imageReads)
	}
}

// Steering and Pi's settings are prepared from the worker's image's labels,
// never the forge's own Docker's, and name where the worker presents the
// files: /forge/in for what the forge sends, /forge/state for Pi's own.
func TestAnAgentOnAWorkerIsSteeredToItsOwnPaths(t *testing.T) {
	f := agentOnWorkerFixture(t, nil, "lsp")
	f.worker.image.Labels["forge.pi-lsp.version"] = "worker-lsp-3"
	req := f.build(t)
	steering := req.steering.Text
	// A change's step on a worker is told the forge merges main in for it
	// (main_merge.go).
	for _, want := range []string{"- /forge/in/attachments/spec.txt", "- /forge/in/resources/res--notes.md", "under /forge/state/sessions", "Delegating (subagent tool)",
		"If main moves, the forge merges it into this change before your step when it can; you don't need to catch up yourself."} {
		if !strings.Contains(steering, want) {
			t.Errorf("the steering lacks %q", want)
		}
	}
	if strings.Contains(steering, ".forge/attachments") || strings.Contains(steering, ".forge/resources") || strings.Contains(steering, ".forge/sessions") {
		t.Errorf("the steering names the folder's paths:\n%s", steering)
	}
	inputs := inputsByPath(t, req.spec)
	if got := string(inputContent(t, inputs["/forge/in/steering.md"])); got != steering || req.steering.Hash != deploy.ConfigHash(steering) {
		t.Error("the steering sent is not the steering recorded")
	}
	settings := string(inputContent(t, inputs["/forge/in/pi-home/.pi/agent/settings.json"]))
	if !strings.Contains(settings, "npm:pi-subagents@"+workerSubagents) || !strings.Contains(settings, "@worker-lsp-3") || strings.Contains(settings, hostSubagents) || strings.Contains(settings, "host-lsp") {
		t.Errorf("pi's settings are not the worker image's: %s", settings)
	}

	// A landing's step is given no merge of main, and is not told it is.
	f = agentOnWorkerFixture(t, nil)
	landing, _ := json.Marshal(schema.Spec{Kind: "pipeline", Role: "landing"})
	f.call.run.DefinitionJSON = string(landing)
	if req := f.build(t); strings.Contains(req.steering.Text, "the forge merges it into this change") {
		t.Error("a landing's step is told the forge merges main in for it")
	}

	// A worker image without the subagent extension is promised none, even
	// though the forge's own image has it.
	f = agentOnWorkerFixture(t, nil)
	delete(f.worker.image.Labels, piharness.SubagentsLabel)
	req = f.build(t)
	if strings.Contains(req.steering.Text, "Delegating") || strings.Contains(strings.Join(req.spec.Argv, " "), "ln -s") {
		t.Error("delegation promised from the forge's own image's labels")
	}
	if _, ok := inputsByPath(t, req.spec)["/forge/in/pi-home/.pi/agent/agents/worker.md"]; ok {
		t.Error("a delegate roster for an image without the extension")
	}
}

// A step needing what the worker cannot provide yet, or whose needs cannot
// be read, is refused from what was admitted before the worker is asked
// anything (its image included), and nothing is made on the forge.
func TestAnAgentStepTheWorkerCannotRunIsRefusedBeforeAnyEffect(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]any
		adjust func(*testing.T, *agentOnWorker)
		reason string
	}{
		{"a service named dind", map[string]any{"docker": true, "services": []map[string]any{{"name": "dind", "image": "busybox"}}}, nil, "cannot be named dind"},
		{"two services of one name", map[string]any{"services": []map[string]any{{"name": "postgres", "image": "busybox"}}, "needs": true}, func(t *testing.T, f *agentOnWorker) {
			f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres: {}\n"})
		}, "two services are named postgres"},
		{"scm", map[string]any{"scm": "write"}, nil, "source-control access"},
		{"its own image", map[string]any{"image": "custom-agent"}, nil, `own agent image "custom-agent"`},
		{"the local executor", map[string]any{"executor": "local"}, nil, `the "local" executor`},
		{"a workflow default executor", nil, func(t *testing.T, f *agentOnWorker) {
			def, _ := json.Marshal(schema.Spec{Kind: "pipeline", Role: "working", Defaults: schema.Defaults{Executor: "local"}})
			f.call.run.DefinitionJSON = string(def)
		}, `the "local" executor`},
		{"an unreadable definition", nil, func(t *testing.T, f *agentOnWorker) { f.call.run.DefinitionJSON = "{" }, "definition could not be read"},
		{"an unreadable step configuration", nil, func(t *testing.T, f *agentOnWorker) { f.call.step.Config = json.RawMessage(`{"services":1}`) }, "configuration could not be read"},
		{"a project without a repository", nil, func(t *testing.T, f *agentOnWorker) { f.call.source.DefaultBranch = "" }, "not repository-backed"},
		{"a need the catalog does not know", map[string]any{"needs": true}, func(t *testing.T, f *agentOnWorker) {
			f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  mongodb: {}\n"})
		}, "the app needs mongodb, which is not in the dependency catalog"},
		{"a malformed declaration", map[string]any{"needs": true}, func(t *testing.T, f *agentOnWorker) {
			f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": "needs: [unterminated\n"})
		}, "could not be read from commit"},
		{"a need parameter that is a list", map[string]any{"needs": true}, func(t *testing.T, f *agentOnWorker) {
			f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": "app:\n  web:\n    build: .\n    public: true\nneeds:\n  postgres:\n    database: [sourdough]\n"})
		}, "needs: postgres: database: a parameter is a single value, not a list"},
		{"need properties that are not a map", map[string]any{"needs": true}, func(t *testing.T, f *agentOnWorker) {
			f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": "app:\n  web:\n    build: .\n    public: true\nneeds:\n  postgres:\n    properties: sourdough\n"})
		}, "needs: postgres: properties: a map of single values, not string"},
		{"a need property that is a list", map[string]any{"needs": true}, func(t *testing.T, f *agentOnWorker) {
			f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": "app:\n  web:\n    build: .\n    public: true\nneeds:\n  postgres:\n    properties:\n      database: [sourdough]\n"})
		}, "needs: postgres: properties: database: a property is a single value, not a list"},
		{"a declaration that is a link", map[string]any{"needs": true}, func(t *testing.T, f *agentOnWorker) {
			f.call.commit = hubCommit(t, f.hub, map[string]string{"elsewhere.yaml": "needs:\n  postgres: {}\n", ".forge/app.yaml": "link:../elsewhere.yaml"})
		}, "not a regular file"},
		{"a commit the hub does not hold", map[string]any{"needs": true}, func(t *testing.T, f *agentOnWorker) {
			f.call.commit = strings.Repeat("ab", 20)
		}, "could not be read from commit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := agentOnWorkerFixture(t, c.config)
			f.worker.imageAllowed = false
			if c.adjust != nil {
				c.adjust(t, f)
			}
			_, err := f.exec.prepareAgentOnWorker(context.Background(), f.call)
			var refusal *WorkerRefusal
			if !errors.As(err, &refusal) || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("refusal: %v (want %q)", err, c.reason)
			}
			if f.worker.imageReads != 0 {
				t.Error("the worker was asked for its image")
			}
			if _, err := os.Lstat(f.call.source.Workspace(f.dir)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the run's folder was made: %v", err)
			}
		})
	}

	// A declaration without needs, or none at all, needs nothing.
	f := agentOnWorkerFixture(t, map[string]any{"needs": true})
	f.build(t)
	f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"})
	f.build(t)
}

// What the worker's image lacks refuses the step once the image is read:
// an image reporting no labels cannot be checked, and an extension the
// project enabled must be one the image carries.
func TestAWorkerImageWithoutWhatIsPromisedRefusesTheStep(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	f.worker.image.Labels = nil
	if _, err := f.exec.prepareAgentOnWorker(context.Background(), f.call); !isRefusal(err, "reported no labels") {
		t.Fatalf("no labels: %v", err)
	}
	f = agentOnWorkerFixture(t, nil, "lsp")
	if _, err := f.exec.prepareAgentOnWorker(context.Background(), f.call); !isRefusal(err, "extensions this project enabled (lsp)") {
		t.Fatalf("a missing extension: %v", err)
	}
	if f.worker.imageReads != 1 {
		t.Errorf("image reads: %d", f.worker.imageReads)
	}
	// Extensions that cannot be read are not "none enabled".
	f = agentOnWorkerFixture(t, nil)
	f.exec.home = piharness.NewPreparer(gatewayModels{}, f.exec.modelSelection, unreadableExtensions{}, f.exec.docker, "forge-agent")
	if _, err := f.exec.prepareAgentOnWorker(context.Background(), f.call); err == nil || !strings.Contains(err.Error(), "reading the project's extensions") {
		t.Fatalf("unreadable extensions: %v", err)
	}
}

type unreadableExtensions struct{}

func (unreadableExtensions) EnabledExtensions(context.Context, ids.ID) ([]string, error) {
	return nil, errors.New("extensions table unavailable")
}

func isRefusal(err error, reason string) bool {
	var refusal *WorkerRefusal
	return errors.As(err, &refusal) && strings.Contains(err.Error(), reason)
}

// A review's request on a worker is its brief, not steering: its argv points
// Pi at the review's own transcript file in the state folder (--session,
// never a lookup by id and never --continue),
// its report is declared a Fresh result of 256 KiB beside the transcript,
// and it has the review's timeout. A large claim is sent as
// /forge/in/review-claim.md and the brief points there; a small one
// travels in the brief.
func TestAReviewOnAWorkerIsSentItsBrief(t *testing.T) {
	f := agentOnWorkerFixture(t, map[string]any{"timeoutMinutes": 0})
	claim := strings.Repeat("claim ", largeClaim/6+1)
	f.call.invocation, f.call.session = "review", "step-00000000-0009-7000-8000-000000000000-a2"
	f.call.review = &reviewCall{cfg: reviewConfig{Of: "report", Model: "fake/reviewer"}, brief: reviewBrief{request: "the request", claim: claim, diff: "the diff"}}
	req := f.build(t)
	inputs := inputsByPath(t, req.spec)
	if in, ok := inputs["/forge/in/review-claim.md"]; !ok || string(inputContent(t, in)) != claim {
		t.Fatal("the large claim was not sent")
	}
	if !strings.Contains(req.claim, "staged at /forge/in/review-claim.md") || len(req.claim) > 200 {
		t.Errorf("the brief's claim: %q", req.claim)
	}
	for path := range inputs {
		if strings.HasPrefix(path, "/forge/in/steering.md") || strings.HasPrefix(path, "/forge/in/resources/") || strings.HasPrefix(path, "/forge/in/attachments/") {
			t.Errorf("a review was steered: %s", path)
		}
	}
	argv := req.spec.Argv
	prompt := argv[len(argv)-1]
	if !strings.Contains(prompt, "the request") || !strings.Contains(prompt, "the diff") || !strings.Contains(prompt, req.claim) || strings.Contains(prompt, claim) {
		t.Errorf("the prompt does not carry the brief with its pointer")
	}
	joined := strings.Join(argv, " ")
	sessionFile := "/forge/state/sessions/" + piharness.SessionFileName("step-00000000-0009-7000-8000-000000000000-a2", f.call.at)
	if !strings.Contains(joined, "--session "+sessionFile+" ") || strings.Contains(joined, "--session-id") || strings.Contains(joined, "--session-dir") || strings.Contains(joined, "--continue") ||
		!strings.Contains(joined, "--no-extensions") || !strings.Contains(joined, "--thinking medium") || strings.Contains(joined, "--append-system-prompt") {
		t.Errorf("the review's argv: %q", argv[4:len(argv)-1])
	}
	want := []execution.Result{
		{Name: "transcript", Path: sessionFile, Max: maxTranscriptResult},
		{Name: "review", Path: "/work/.forge/review.json", Max: 256 << 10, Fresh: true},
	}
	if !slices.Equal(req.spec.Results, want) || req.spec.Key != "step-00000000-0009-7000-8000-000000000000-attempt-2-review" {
		t.Errorf("the review's results: %+v %q", req.spec.Results, req.spec.Key)
	}
	if req.spec.Admission.Limits.WallClock != reviewTimeout {
		t.Errorf("the review's timeout: %v", req.spec.Admission.Limits.WallClock)
	}
	f.call.review.brief.claim = "a small claim"
	req = f.build(t)
	if _, ok := inputsByPath(t, req.spec)["/forge/in/review-claim.md"]; ok || req.claim != "a small claim" {
		t.Errorf("a small claim: %q", req.claim)
	}
}

// A review's wrap-up or fixup is the admitted request under its own key,
// with its order as the message and its fixed allowance recorded as its
// budget; the session, inputs and result files are the review's. An
// agent's retry is the same request with the step's own timeout.
func TestAFollowingInvocationIsTheAdmittedRequestWithItsOrder(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	f.call.invocation = "review"
	f.call.review = &reviewCall{cfg: reviewConfig{}, brief: reviewBrief{claim: "c"}}
	req := f.build(t)
	kept := admittedRequest{Task: req.spec, Agent: &agentAdmission{Invocation: "review", Number: 1, Timeout: reviewTimeout, Review: &reviewAdmission{Claim: "c"}}}
	now := f.call.at.Add(11 * time.Minute)
	for next, want := range map[string]struct {
		order  string
		budget time.Duration
	}{"review-wrapup": {reviewWrapUp, 5 * time.Minute}, "review-fixup": {reviewFixUp, 4 * time.Minute}} {
		spec, meta := followingRequest(kept, "key-"+next, next, now)
		argv := spec.Argv
		if argv[len(argv)-1] != want.order || !slices.Equal(argv[:len(argv)-1], req.spec.Argv[:len(argv)-1]) {
			t.Errorf("%s's argv: %q", next, argv[len(argv)-1])
		}
		if meta.Invocation != next || meta.Number != 2 || meta.Timeout != want.budget || spec.Admission.Limits.WallClock != want.budget ||
			!spec.Admission.Expires.Equal(now.Add(want.budget+taskGrace)) || spec.Key != "key-"+next || meta.Review.Claim != "c" {
			t.Errorf("%s: %+v %+v", next, meta, spec.Admission)
		}
		if !slices.Equal(spec.Results, req.spec.Results) || len(spec.Inputs) != len(req.spec.Inputs) {
			t.Errorf("%s changed the review's inputs or results", next)
		}
	}
	if req.spec.Argv[len(req.spec.Argv)-1] == reviewWrapUp {
		t.Error("the admitted request was changed")
	}
	agent := admittedRequest{Task: req.spec, Agent: &agentAdmission{Invocation: "agent", Number: 1, Timeout: 20 * time.Minute}}
	spec, meta := followingRequest(agent, "k", "agent-retry", now)
	if !slices.Equal(spec.Argv, req.spec.Argv) || meta.Timeout != 20*time.Minute || !spec.Admission.Expires.Equal(now.Add(20*time.Minute+taskGrace)) {
		t.Errorf("the agent's retry: %+v", meta)
	}
}

// A continued session is sent its stored transcript, under the name the
// attempt's first invocation gave it; a transcript of another session is
// not sent.
func TestAContinuedSessionIsSentItsTranscript(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	first := f.call.at
	stored := append(piharness.SessionHeader("step-00000000-0009-7000-8000-000000000000-a2", first.Add(-time.Hour), "/work"), []byte(`{"type":"message"}`+"\n")...)
	f.call.transcript = stored
	f.call.invocation, f.call.sessionAt, f.call.at = "agent-retry", first, first.Add(10*time.Minute)
	req := f.build(t)
	name := piharness.SessionFileName("step-00000000-0009-7000-8000-000000000000-a2", first)
	if in, ok := inputsByPath(t, req.spec)["/forge/in/sessions/"+name]; !ok || !bytes.Equal(inputContent(t, in), stored) {
		t.Fatal("the stored transcript was not sent under the attempt's name")
	}
	if req.spec.Key != "step-00000000-0009-7000-8000-000000000000-attempt-2-agent-retry" || req.spec.SharedWorkspace != "step-00000000-0009-7000-8000-000000000000-a2" || req.spec.Results[0].Path != "/forge/state/sessions/"+name {
		t.Errorf("the retry: %q %q %+v", req.spec.Key, req.spec.SharedWorkspace, req.spec.Results)
	}
	f.call.transcript = piharness.SessionHeader("step-00000000-0009-7000-8000-000000000000-a1", first, "/work")
	if _, err := f.exec.prepareAgentOnWorker(context.Background(), f.call); err == nil {
		t.Fatal("another session's transcript was sent")
	}
}

// A run's folder whose .forge is a link to somewhere else offers the
// forge nothing it reads and gets nothing it writes: the request is built
// without the folder.
func TestAFolderLinkChangesNothingInTheRequest(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(elsewhere, "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"steering.md", "resources/res--notes.md", "review-claim.md"} {
		if err := os.WriteFile(filepath.Join(elsewhere, name), []byte(evilFolderSteering), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	folder := f.call.source.Workspace(f.dir)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(folder, ".forge")); err != nil {
		t.Fatal(err)
	}
	before := listTree(t, elsewhere)
	req := f.build(t)
	for _, in := range req.spec.Inputs {
		if strings.Contains(string(inputContent(t, in)), evilFolderSteering) {
			t.Errorf("the input %s came from the folder's link", in.Path)
		}
	}
	if after := listTree(t, elsewhere); !slices.Equal(before, after) {
		t.Errorf("the link's target changed:\n%v\n%v", before, after)
	}
	if after := listTree(t, folder); !slices.Equal(after, []string{".forge"}) {
		t.Errorf("the folder changed: %v", after)
	}
}

func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, rel+" "+info.Mode().String()+" "+info.ModTime().String())
		if d.Type()&os.ModeSymlink != 0 {
			out[len(out)-1] = rel
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// An agent on the forge's host is steered as before: to the run's folder's
// .forge paths, with what the forge's own agent image carries, and its
// resources and steering written into the folder it is given.
func TestAnAgentOnTheForgeIsSteeredToTheFolder(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".forge", "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	var cfg agentConfig
	if err := json.Unmarshal(f.call.step.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	st, err := f.exec.buildSteering(t.Context(), f.call.run, f.call.step, &cfg, ws, f.exec.resolveAgentKnobs(f.call.run, &cfg))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- .forge/attachments/spec.txt", "- .forge/resources/res--notes.md", "under .forge/sessions", "Delegating (subagent tool)"} {
		if !strings.Contains(st.Text, want) {
			t.Errorf("the steering lacks %q", want)
		}
	}
	if strings.Contains(st.Text, "/forge/in") || strings.Contains(st.Text, "/forge/state") {
		t.Error("an agent on the forge is steered to a worker's paths")
	}
	if strings.Contains(st.Text, "the forge merges it into this change") {
		t.Error("an agent on the forge, which is given no merge of main, is told it is")
	}
	if body, err := os.ReadFile(filepath.Join(ws, ".forge", "resources", "res--notes.md")); err != nil || string(body) != "resource body" {
		t.Errorf("the resource in the folder: %q %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(ws, ".forge", "steering.md")); err != nil || string(body) != st.Text {
		t.Errorf("the steering in the folder: %v", err)
	}
	if f.worker.imageReads != 0 {
		t.Error("an agent on the forge asked the worker for its image")
	}
}

// postgresKind is a catalog kind for postgres whose image is at version.
func postgresKind(version string) string {
	return "capability: postgres\ntitle: PostgreSQL\ndefaults:\n  database: app\nlocal:\n  image: postgres:" + version + "-alpine\n" +
		"  health: pg_isready -d {database}\n  env:\n    POSTGRES_DB: \"{database}\"\n  inject:\n    DATABASE_URL: \"postgres://{host}/{database}\"\n"
}

// A step with private Docker, services and needs is admitted on the worker
// with them frozen in its request: its declared services, and the app's
// needs as the admitted commit declares them (parameters included),
// resolved against the catalog now, with the environment reaching them;
// a daemon; limits that are never zero; the integration environment as on
// the forge's host; and "unqualified" for its evidence. They are part of
// the request's identity: the same step admitted after the catalog changed
// is another request, so only the kept one is ever sent again.
func TestPrivateDockerOnTheWorkerIsFrozenInTheRequest(t *testing.T) {
	f := agentOnWorkerFixture(t, map[string]any{"docker": true, "needs": true, "services": []map[string]any{{"name": "cache", "image": "redis:7-alpine"}}})
	f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    database: sourdough\n"})
	req := f.build(t)
	p := req.spec.Private
	if p == nil || !p.Daemon || len(p.Services) != 2 || p.Services[0].Name != "cache" || p.Services[1].Name != "postgres" ||
		p.Services[1].Image != "postgres:16-alpine" || p.Services[1].Env["POSTGRES_DB"] != "sourdough" {
		t.Fatalf("private Docker: %+v", p)
	}
	env := strings.Join(req.spec.Env, "\n")
	if !strings.Contains(env, "DATABASE_URL=postgres://postgres/sourdough") || strings.Contains(env, "DOCKER_HOST") {
		t.Errorf("env: the need's connection, and nothing of the worker's daemon: %s", env)
	}
	if l := req.spec.Admission.Limits; l.CPUs <= 0 || l.MemoryBytes <= 0 || l.PIDs <= 0 {
		t.Errorf("limits: %+v", l)
	}
	if !strings.HasPrefix(req.privateDocker, "unqualified") || req.profile == nil || *req.profile != workerProfile || p.Profile != req.profile {
		t.Errorf("evidence: %q, the profile it was admitted on: %+v, in the request: %+v", req.privateDocker, req.profile, p.Profile)
	}
	if !strings.Contains(req.steering.Text, "Your private Docker") || strings.Contains(req.steering.Text, "preloaded") {
		t.Error("the steering does not say what the agent has")
	}
	if _, err := f.exec.dependencies.Save(context.Background(), postgresKind("17"), "tester"); err != nil {
		t.Fatal(err)
	}
	again := f.build(t)
	if again.spec.Private.Services[1].Image != "postgres:17-alpine" || again.spec.Digest() == req.spec.Digest() {
		t.Fatalf("a catalog change is not part of the request's identity: %+v", again.spec.Private)
	}

	// A step with none of these has none; a review never has them.
	f = agentOnWorkerFixture(t, nil)
	if req := f.build(t); req.spec.Private != nil || req.privateDocker != "" {
		t.Fatalf("private Docker for a step declaring none: %+v", req.spec.Private)
	}
}

// Private Docker on a worker is an operator's opt-in (-private-docker):
// without it, a step declaring docker: true is refused with the reason
// before the worker is asked anything. Services alone, which run no
// privileged daemon, need no opt-in, and are admitted on the profile the
// worker reports.
func TestPrivateDockerOnAWorkerNeedsTheOperatorsOptIn(t *testing.T) {
	f := agentOnWorkerFixture(t, map[string]any{"docker": true})
	f.exec.cfg.PrivateDockerOnWorker = false
	f.worker.imageAllowed = false
	_, err := f.exec.prepareAgentOnWorker(context.Background(), f.call)
	var refusal *WorkerRefusal
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Reason, "private Docker on a worker is the operator's opt-in: start the forge with -private-docker") {
		t.Fatalf("without the opt-in: %v", err)
	}
	if f.worker.imageReads != 0 {
		t.Errorf("the worker was asked %d times", f.worker.imageReads)
	}

	f = agentOnWorkerFixture(t, map[string]any{"services": []map[string]any{{"name": "cache", "image": "redis:7-alpine"}}})
	f.exec.cfg.PrivateDockerOnWorker = false
	req := f.build(t)
	if p := req.spec.Private; p == nil || p.Daemon || p.Profile == nil || *p.Profile != workerProfile || !strings.HasPrefix(req.privateDocker, "unqualified") {
		t.Fatalf("services without the opt-in: %+v %q", p, req.privateDocker)
	}
}

// An invocation with private Docker admitted on a worker whose measured
// profile matches a qualified fact set records "qualified" with that
// qualification's record; with any of those facts different (here the
// daemon's image), it records "unqualified". The opt-in is needed either
// way.
func TestPrivateDockerRecordsWhetherItsProfileIsQualified(t *testing.T) {
	qualified, err := execution.QualifiedProfiles()
	if err != nil || len(qualified) == 0 {
		t.Fatalf("no qualified profile to admit on: %v", err)
	}
	q := qualified[0]
	measured := execution.WorkerProfile{Name: q.Name, OS: q.OS, Architecture: q.Architecture, Kernel: "6.17.2-1-pve", Cgroup: q.Cgroup, Docker: q.DockerMajor + ".1.5",
		StorageDriver: q.StorageDriver, DindImage: "docker:27-dind", DindImageID: "sha256:d8", DindDigest: q.DindDigest, DindVersion: q.DindVersion,
		Privileged: "yes", Exclusive: true, Egress: "not contained"}
	f := agentOnWorkerFixture(t, map[string]any{"docker": true})
	f.worker.reported = &measured
	if req := f.build(t); req.privateDocker != "qualified: worker-vm ("+q.Record+")" || req.profile == nil || *req.profile != measured {
		t.Errorf("on a qualified profile: %q, admitted on %+v", req.privateDocker, req.profile)
	}

	changed := measured
	changed.DindDigest = "docker@sha256:another"
	f = agentOnWorkerFixture(t, map[string]any{"docker": true})
	f.worker.reported = &changed
	if req := f.build(t); !strings.HasPrefix(req.privateDocker, "unqualified") {
		t.Errorf("on a profile matching none: %q", req.privateDocker)
	}

	f = agentOnWorkerFixture(t, map[string]any{"docker": true})
	f.worker.reported, f.exec.cfg.PrivateDockerOnWorker, f.worker.imageAllowed = &measured, false, false
	var refusal *WorkerRefusal
	if _, err := f.exec.prepareAgentOnWorker(context.Background(), f.call); !errors.As(err, &refusal) {
		t.Errorf("a qualified profile without the opt-in: %v", err)
	}
}
