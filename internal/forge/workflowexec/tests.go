package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/strs"
	"flokkit.io/forge/internal/testreport"
)

// The tests gate (design/the-change.md: boundaries are where invariants
// live). "The tree that merges ran its tests" is a RECEIPT on the change,
// not a pipeline somewhere else: the step stages the COMMITTED tree (dirty
// files cannot pass tests on their way out — same posture as scans and
// releases), detects or is told the test command, runs it in the agent
// container with the ambient toolchain, and records command, exit, and
// output as evidence. A project with no tests passes vacuously AND SAYS SO
// — unless the workflow declares require: true.

const testsTimeout = 10 * time.Minute

// The test report (internal/testreport): the command may write one at
// APPTOWN_TEST_REPORT, which the execution layer carries back from wherever
// the tests ran. Its counts are recorded; the exit code alone decides.
const (
	testReportEnv  = "APPTOWN_TEST_REPORT"
	testReportFile = ".forge-test-report"
	testReportPath = "/work/" + testReportFile
	skippedShown   = 20
)

// detectTestCommand: the conventional test entrypoints, most specific
// first. Detection is deliberately small and named in evidence; anything
// unusual belongs in the step's `command` config.
func detectTestCommand(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		// Go's own report format: its stream gives the counts, and the
		// receipt's log is rendered from it.
		return `go test -json ./... > "$` + testReportEnv + `"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(b, &pkg) == nil {
			if t := pkg.Scripts["test"]; t != "" && !strings.Contains(t, "no test specified") {
				return "npm ci --no-audit --no-fund >/dev/null 2>&1 && npm test"
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "Makefile")); err == nil {
		if regexp.MustCompile(`(?m)^test:`).Match(b) {
			return "make test"
		}
	}
	// Maven arrives via the repo's mise.toml pin (the java pack's declared
	// way of working — no wrapper scripts); the local repo lives in the
	// shared cache volume so dependency downloads survive across runs.
	if _, err := os.Stat(filepath.Join(dir, "pom.xml")); err == nil {
		return "mvn -q -Dmaven.repo.local=/apptown-cache/m2 test"
	}
	return ""
}

// RunTests executes a tests step.
func (a *Commands) RunTests(task *workflow.Execution) {
	defer reportPanic(a.panics, "RunTests")
	step := task.Step()
	stepID := step.ID
	run := task.Run()
	var cfg testsConfig
	cfgErr := workflow.DecodeStepConfig(step.Config, &cfg)

	finish := func(status workflow.StepState, evidence map[string]any) {
		task.Settle(workflow.EncodeOutcome(status, evidence, nil))
	}
	if _, err := task.Claim(); err != nil {
		return
	}
	if cfgErr != nil {
		finish(workflow.StatusError, map[string]any{"reason": cfgErr.Error()})
		return
	}
	stopHB := a.progress.Heartbeat(stepID, "running tests")
	defer stopHB()

	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	// One commit, selected as the change's views and guidance checks select
	// it, resolved once: staging and evidence name the same one. Source that
	// is missing or cannot be read is an error, never a pass.
	sel, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, run, source)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "selecting the commit to test: " + err.Error()})
		return
	}
	report := finish
	finish = func(status workflow.StepState, evidence map[string]any) {
		evidence["commit"] = sel.Commit
		if sel.Candidate != "" {
			// A landing tests its pinned candidate, and every outcome names it.
			evidence["candidate"] = sel.Candidate
		}
		report(status, evidence)
	}
	staged, cleanup, err := verification.StageCommittedTree(task.Context(), a.cfg.DataDir, sel.Repo, sel.Commit)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "staging the committed tree: " + err.Error()})
		return
	}
	defer cleanup()
	tree := strs.ShortSHA(sel.Commit)

	command := cfg.Command
	detected := false
	if command == "" {
		command = detectTestCommand(staged)
		detected = command != ""
	}
	if command == "" {
		if cfg.Require {
			finish(workflow.StatusFail, map[string]any{
				"reason": "this workflow requires tests, and none were found — no go.mod, no package.json test script, no Makefile test target, no pom.xml. Add tests, or set the step's command."})
			return
		}
		finish(workflow.StatusPass, map[string]any{
			"reason": "no tests found — nothing to run (go.mod, package.json test script, Makefile test target, or pom.xml would be detected)",
			"tree":   tree})
		return
	}

	timeout := testsTimeout
	if cfg.TimeoutMinutes > 0 {
		timeout = time.Duration(cfg.TimeoutMinutes) * time.Minute
	}
	// The ambient build environment (design/subsystems/registries.md): registry env +
	// generated mise config, same as script steps — a mise-pinned toolchain
	// installs through the mirrors (the image bakes only Node and Python;
	// Go, Maven and the rest come from the declaration). MISE_DATA_DIR's
	// folder is the container's own; the download cache is shared (the
	// executor adds a cache volume here, the worker its tool cache). Its
	// files are generated beside the tree, never in it, and the base rerun
	// below is given the same ones.
	amb := agentenv.AmbientFor(integration.RegistryBindingsIn(task.Context(), a.db), staged)
	env := append([]string{}, amb.Env...)
	// The app's declared needs, as the tested commit declares them, run
	// beside the tests, which are given the environment the app is given
	// to reach them (DATABASE_URL and the like): an app's tests that use
	// its database find one. A need the catalog cannot fulfil is named in
	// the evidence, and the tests run without it.
	services, needEnv, unfulfilled, specPath, err := a.testServices(task.Context(), staged)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error(), "tree": tree})
		return
	}
	env = append(env, needEnv...)
	// The app's declared secrets: a generated one (a framework's signing
	// key) gets a throwaway value minted for this run, so settings that
	// need it load; a real credential is not given to the tests. Only the
	// names reach the evidence.
	secretEnv, secretsGiven, secretsWithheld, err := secret.TestsEnv(staged, specPath)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "the app's declared secrets could not be read: " + err.Error(), "tree": tree})
		return
	}
	env = append(env, secretEnv...)
	checks := a.checks
	if checks.Run == nil {
		checks = CompileCheck{Run: verification.LocalChecks(a.runner, a.clock), Image: a.cfg.Image}
	}
	// A tests check reaches the network (installs need it) and writes its
	// scratch tree; it publishes nothing.
	runIn := func(dir, label string) execution.Outcome {
		// A report committed with the tree is not this run's.
		if err := os.Remove(filepath.Join(dir, testReportFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: fmt.Errorf("removing the tree's own %s: %w", testReportFile, err)}
		}
		return checks.Run(task.Context(), execution.Request{
			Workspace: dir,
			// Shims go on PATH only when the install SUCCEEDED: a shim for a tool
			// mise couldn't install shadows the image-baked toolchain and re-fails
			// at exec time: a tests gate can say "continuing with image
			// toolchains" while a node shim blocks the image's node from running.
			Argv:     []string{"sh", "-c", testsScript(command)},
			Image:    checks.Image,
			Timeout:  timeout,
			Name:     execution.UniqueName(fmt.Sprintf("forge-tests-%d%s", stepID, label)),
			Files:    amb.Files,
			Env:      append(env[:len(env):len(env)], testReportEnv+"="+testReportPath),
			Services: services,
			Report:   testReportFile,
		})
	}
	start := a.now()
	outcome := runIn(staged, "")
	evidence := map[string]any{
		"command":    command,
		"detected":   detected,
		"tree":       tree,
		"durationMs": a.now().Sub(start).Milliseconds(),
	}
	log := string(outcome.Output)
	if outcome.Answered() {
		log = recordTestReport(evidence, outcome, log)
	}
	if len(log) > 4000 {
		log = log[len(log)-4000:]
	}
	evidence["logTail"] = log
	if len(services) > 0 {
		names := make([]string, len(services))
		for i, svc := range services {
			names[i] = svc.Name + " (" + svc.Image + ")"
		}
		evidence["services"] = names
	}
	if len(secretsGiven) > 0 {
		evidence["secretsProvided"] = "throwaway values for this run (never the deployed ones): " + strings.Join(secretsGiven, ", ")
	}
	if len(secretsWithheld) > 0 {
		evidence["secretsNotProvided"] = "the app declares " + strings.Join(secretsWithheld, ", ") + " with no generated or default value; credentials are not given to tests"
	}
	if len(unfulfilled) > 0 {
		evidence["unfulfilled"] = "the app needs " + strings.Join(unfulfilled, ", ") + ", which the dependency catalog cannot run beside the tests; they ran without it"
	}
	switch {
	case toolchainUnavailable(outcome):
		// The tests never had their tools: AppTown's error, not a
		// failure of the change ("mvn: not found" must not read as "tests
		// failed").
		evidence["outcome"] = "toolchain_unavailable"
		evidence["reason"] = "the toolchain this project declares could not be installed, so the tests could not run; the installer's output is in the log. This is not a failure of the change: run the checks again"
		finish(workflow.StatusError, evidence)
	case !outcome.Answered():
		// Only a witnessed ordinary exit answers. A run that could not start
		// (its generated files among the reasons), timed out, was cancelled,
		// left its container behind, or whose answer was lost says nothing
		// about the tests: AppTown's error, never a failure of the tests,
		// never a pass, and no base run can make it carried debt.
		evidence["outcome"] = outcomeKind(outcome)
		evidence["reason"] = unansweredReason(outcome, timeout)
		finish(workflow.StatusError, evidence)
	case outcome.ExitCode != 0:
		// Provenance, as for scans and guidance checks (N32): a failure that
		// reproduces on the change's base commit is the estate's debt, not
		// this change's — greeting-api's Testcontainers suite needs a Docker
		// daemon the step has none of, failed on main exactly as on the
		// change, and wedged a README fix and a readiness fix on it.
		if base := verification.WorkflowProvenanceBase(task.Context(), a.cfg.DataDir, source); base != "" {
			switch carried, why := testsFailOnBase(task.Context(), a.cfg.DataDir, source.Repository(a.cfg.DataDir), base, command, detected, runIn); {
			case carried:
				short := base[:min(8, len(base))]
				evidence["carried"] = "the same tests fail on the base commit " + short + " — the estate's debt, not this change's; it is reported, not gating"
				evidence["baseCommit"] = short
				finish(workflow.StatusPass, evidence)
				return
			case why != "":
				evidence["base"] = why // not known on the base: the failure stands
			}
		}
		evidence["outcome"] = "exited"
		evidence["reason"] = "tests failed: " + command
		finish(workflow.StatusFail, evidence)
	default:
		evidence["outcome"] = "exited"
		finish(workflow.StatusPass, evidence)
	}
}

// recordTestReport records what the run's test report says in evidence:
// its counts and skipped tests, or why they are not known. It answers the
// receipt's log: the command's output, then a Go stream's rendered log.
// The verdict never reads it.
func recordTestReport(evidence map[string]any, o execution.Outcome, output string) string {
	// testReportUnreadable is the reason alone, for the views to show
	// without reading the sentence.
	unreadable := func(err error) string {
		evidence["testReport"] = "the test report could not be read (" + err.Error() + "), so how many tests ran or were skipped is not known"
		evidence["testReportUnreadable"] = err.Error()
		return output
	}
	if o.ReportErr != nil {
		return unreadable(o.ReportErr)
	}
	r, err := testreport.Read(o.Report)
	switch {
	case errors.Is(err, testreport.ErrEmpty):
		evidence["testReport"] = "the test command wrote no report to $" + testReportEnv + ", so how many tests ran or were skipped is not known"
		return output
	case err != nil:
		return unreadable(err)
	}
	evidence["testCounts"] = r.Counts
	evidence["testReportFormat"] = r.Format
	if len(r.Skipped) > 0 {
		evidence["skippedTests"] = r.Skipped[:min(len(r.Skipped), skippedShown)]
	}
	if r.Log == "" {
		return output
	}
	if strings.TrimSpace(output) == "" {
		return r.Log
	}
	return strings.TrimRight(output, "\n") + "\n" + r.Log
}

// testServices are the services fulfilling the app's declared needs
// (.apptown/app.yaml in the staged tree), with the environment that reaches
// them, the needs the catalog does not know, and where the app's secret
// declaration is. No declaration is no needs; one that cannot be read
// leaves the tests' setting unknown.
func (a *Commands) testServices(ctx context.Context, staged string) (services []deploy.ServiceSpec, env, unknown []string, specPath string, err error) {
	c, err := docker.ReadWorkspaceContract(staged)
	switch {
	case errors.Is(err, docker.ErrNoContract):
		return nil, nil, nil, "", nil
	case err != nil:
		return nil, nil, nil, "", fmt.Errorf("the app's declared needs (.apptown/app.yaml) could not be read: %w", err)
	case len(c.Needs) == 0:
		return nil, nil, nil, c.SecretSpecPath, nil
	case a.catalog == nil:
		return nil, nil, nil, "", errors.New("the app declares needs, and this AppTown has no dependency catalog to fulfil them")
	}
	services, needEnv, err := (dependency.LocalRuntime{Catalog: a.catalog, Steps: true}).Fulfil(ctx, c.Needs, c.NeedParams)
	var unknownNeeds *dependency.UnknownNeeds
	switch {
	case errors.As(err, &unknownNeeds):
		// The tests run without what the catalogue does not hold, and the
		// evidence names it.
		unknown = unknownNeeds.Needs
	case err != nil:
		return nil, nil, nil, "", err
	}
	env = make([]string, 0, len(needEnv))
	for k, v := range needEnv {
		env = append(env, k+"="+v)
	}
	slices.Sort(env)
	return services, env, unknown, c.SecretSpecPath, nil
}

// outcomeKind names a run that gave no answer, for the evidence.
// toolchainUnavailableExit is the tests script's exit when the command
// failed after the declared toolchain did not install (script steps use the
// same code for a failed install).
const toolchainUnavailableExit = 97

const toolchainUnavailableMark = "apptown: the toolchain this project declares could not be installed"

// testsScript runs command over the tree with the declared toolchain
// installed first. A failed install does not stop the run: the image's own
// toolchains may serve (and a shim for a tool mise could not install would
// shadow them, so shims go on PATH only after a successful install — run
// 182). But if the command then fails, the failure is the missing
// toolchain's, not the tests': the script says so and exits
// toolchainUnavailableExit.
func testsScript(command string) string {
	return `mise trust -q 2>/dev/null; forge_toolchain=ok; ` +
		`if mise install -y -q; then export PATH="$MISE_DATA_DIR/shims:$PATH"; else forge_toolchain=failed; echo "apptown: mise install failed (continuing with image toolchains)" >&2; fi; ` +
		`(` + command + `) 2>&1; rc=$?; ` +
		// A command that wrote no report leaves an empty one, so "no report"
		// reads the same from a worker as from here (a missing file's error
		// travels back as text).
		`[ -n "$` + testReportEnv + `" ] && : 2>/dev/null >> "$` + testReportEnv + `"; ` +
		`if [ "$rc" -ne 0 ] && [ "$forge_toolchain" = failed ]; then echo "` + toolchainUnavailableMark + `" >&2; exit ` + fmt.Sprint(toolchainUnavailableExit) + `; fi; exit "$rc"`
}

// toolchainUnavailable reports whether a tests run failed for want of its
// declared toolchain (testsScript), which says nothing about the tests.
func toolchainUnavailable(o execution.Outcome) bool {
	return o.Answered() && o.ExitCode == toolchainUnavailableExit && strings.Contains(string(o.Output), toolchainUnavailableMark)
}

func outcomeKind(o execution.Outcome) string {
	switch {
	case o.Termination == execution.Exited && o.CleanupErr != nil:
		return "cleanup_failed"
	case o.Termination == "":
		return "no_answer"
	}
	return string(o.Termination)
}

func unansweredReason(o execution.Outcome, timeout time.Duration) string {
	switch outcomeKind(o) {
	case string(execution.LaunchFailed):
		return "the tests could not be started: " + errText(o.RunErr)
	case string(execution.TimedOut):
		return fmt.Sprintf("the tests did not finish within %s, so they gave no answer", timeout)
	case string(execution.Canceled):
		return "the tests were stopped before they finished, so they gave no answer: " + errText(o.RunErr)
	case "cleanup_failed":
		return fmt.Sprintf("the tests exited %d, but their container could not be cleaned up, so the run is not an answer: %v", o.ExitCode, o.CleanupErr)
	case string(execution.OutOfMemory):
		return "the tests ran out of memory, so they gave no answer: " + errText(o.RunErr) + "; raise apptownd -task-memory-mib if the suite needs more"
	}
	return "the tests gave no answer: " + errText(o.RunErr)
}

// testsFailOnBase runs the same test command over the change's base commit,
// from the project's repository, and reports whether it was witnessed
// failing there too; otherwise why the base is not known. Only reached on a
// witnessed failure, so a passing suite costs nothing extra. A detected
// command the base does not have (the change added the Makefile's test
// target, say) is no tests at the base: the base run would fail for want
// of the command, not of the tests, so every failure is the change's.
func testsFailOnBase(ctx context.Context, dataDir, repo, base, command string, detected bool, runIn func(dir, label string) execution.Outcome) (bool, string) {
	dir, cleanup, err := verification.StageCommittedTree(ctx, dataDir, repo, base)
	if err != nil {
		return false, "the base commit could not be staged: " + err.Error()
	}
	defer cleanup()
	if detected && detectTestCommand(dir) != command {
		return false, "the base commit has no " + command + " to run, so no tests fail there: every failure is this change's"
	}
	out := runIn(dir, "-base")
	// Only a witnessed failure on the base carries: the base's command ran
	// and exited non-zero. One that could not start, timed out or left its
	// run unsettled says nothing about the base.
	if !out.Answered() {
		return false, "the base commit's tests gave no answer (" + outcomeKind(out) + ")"
	}
	if toolchainUnavailable(out) {
		return false, "the base commit's toolchain could not be installed, so its tests gave no answer"
	}
	return out.ExitCode != 0, ""
}

func errText(err error) string {
	if err == nil {
		return "no reason given"
	}
	return err.Error()
}

// testsConfig is a tests step's options (schema.StepOptions("tests")).
type testsConfig struct {
	Command        string `json:"command"`
	Require        bool   `json:"require"`
	TimeoutMinutes int    `json:"timeoutMinutes"`
}
