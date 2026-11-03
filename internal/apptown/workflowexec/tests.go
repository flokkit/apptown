package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/testreport"
	"github.com/flokkit/apptown/internal/trust"
)

// The tests step stages one committed repository and runs every part's declared
// test commands. Applicability determines missing declarations, not which commands run.

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

// RunTests executes a tests step.
func (a *commands) RunTests(task *workflow.Execution) {
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

	declarations, status, reason := declaredTests(cfg, staged)
	if status == workflow.StatusError || len(declarations) == 0 {
		finish(status, map[string]any{"reason": reason, "tree": tree})
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
	// files are generated beside the tree, never in it.
	bound, err := integration.RegistryBindings(task.Context(), a.db)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error(), "tree": tree})
		return
	}
	amb := agentenv.AmbientFor(bound, staged)
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
	runIn := func(declaration testDeclaration) execution.Outcome {
		// A report committed with the tree is not this run's.
		if err := os.Remove(filepath.Join(staged, testReportFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: fmt.Errorf("removing the tree's own %s: %w", testReportFile, err)}
		}
		return checks.Run(task.Context(), execution.Request{
			Workspace: staged,
			// Shims go on PATH only when the install SUCCEEDED: a shim for a tool
			// mise couldn't install shadows the image-baked toolchain and re-fails
			// at exec time: a tests gate can say "continuing with image
			// toolchains" while a node shim blocks the image's node from running.
			Argv:     []string{"sh", "-c", testsScript("cd " + shellQuote(declaration.Folder) + " && sh -c " + shellQuote(declaration.Command))},
			Image:    checks.Image,
			Timeout:  timeout,
			Name:     execution.UniqueName(execution.StepContainer(stepID, "tests")),
			Files:    amb.Files,
			Env:      append(env[:len(env):len(env)], testReportEnv+"="+testReportPath),
			Services: services,
			Report:   testReportFile,
		})
	}
	evidence := map[string]any{"tree": tree}
	var results []map[string]any
	var logs []string
	var summary map[string]any
	rank := map[workflow.StepState]int{workflow.StatusPass: 0, workflow.StatusFail: 1, workflow.StatusError: 2}
	for _, declaration := range declarations {
		start := a.now()
		outcome := runIn(declaration)
		result := map[string]any{
			"part": declaration.Part, "folder": declaration.Folder,
			"command": declaration.Command, "exitCode": outcome.ExitCode,
			"durationMs": a.now().Sub(start).Milliseconds(), "output": string(outcome.Output),
		}
		log := string(outcome.Output)
		if outcome.Answered() {
			log = recordTestReport(result, outcome, log)
		}
		if len(log) > 4000 {
			log = log[len(log)-4000:]
		}
		if strings.TrimSpace(log) == "" && outcome.Answered() && outcome.ExitCode != 0 {
			log = check.ReportLost("the tests")
		}
		result["logTail"] = log
		logs = append(logs, declaration.Part+": "+declaration.Command+"\n"+log)
		commandStatus := workflow.StatusPass
		switch {
		case toolchainUnavailable(outcome):
			commandStatus = workflow.StatusError
			result["outcome"] = "toolchain_unavailable"
			result["reason"] = "the toolchain this project declares could not be installed, so the tests could not run; the installer's output is in the log. This is not a failure of the change: run the checks again"
		case !outcome.Answered():
			commandStatus = workflow.StatusError
			result["outcome"] = outcomeKind(outcome)
			result["reason"] = unansweredReason(outcome, timeout)
		default:
			result["outcome"] = "exited"
			if outcome.ExitCode != 0 {
				commandStatus = workflow.StatusFail
				result["reason"] = "tests failed: " + declaration.Part + ": " + declaration.Command
			}
		}
		result["status"] = commandStatus
		// Keep the first command at the worst status. Missing declarations
		// can set the status without providing a command summary.
		if rank[commandStatus] > rank[status] {
			status = commandStatus
			summary = nil
		}
		if summary == nil && rank[commandStatus] == rank[status] {
			summary = result
		}
		results = append(results, result)
	}
	for k, v := range summary {
		evidence[k] = v
	}
	commandReason, _ := summary["reason"].(string)
	var reasons []string
	for _, r := range []string{reason, commandReason} {
		if r != "" {
			reasons = append(reasons, r)
		}
	}
	if len(reasons) > 0 {
		evidence["reason"] = strings.Join(reasons, "; ")
	}
	if len(results) > 1 {
		// One command's counts cannot describe all the suites.
		for _, key := range []string{"testCounts", "testReportFormat", "skippedTests", "testReportUnreadable"} {
			delete(evidence, key)
		}
		evidence["testReport"] = "reports are recorded per command; combined test counts are not known"
	}
	evidence["commands"] = results
	evidence["logTail"] = strings.Join(logs, "\n")

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
	finish(status, evidence)
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
func (a *commands) testServices(ctx context.Context, staged string) (services []deploy.ServiceSpec, env, unknown []string, specPath string, err error) {
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
	return trust.Run + `; mise trust -q 2>/dev/null; forge_toolchain=ok; ` +
		`if mise install -y -q; then export PATH="$MISE_DATA_DIR/shims:$PATH"; ` + trust.Run + `; else forge_toolchain=failed; echo "apptown: mise install failed (continuing with image toolchains)" >&2; fi; ` +
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

func errText(err error) string {
	if err == nil {
		return "no reason given"
	}
	return err.Error()
}

// testsConfig is a tests step's options (schema.StepOptions("tests")).
type testsConfig struct {
	Require        bool `json:"require"`
	TimeoutMinutes int  `json:"timeoutMinutes"`
	// AppliesTo identifies parts required to declare tests. Every declaration runs.
	AppliesTo []string `json:"appliesTo"`
}

type testDeclaration struct{ Part, Folder, Command string }

// declaredTests keeps the obligation per part, even where parts share a folder.
// Missing declarations do not prevent other parts' commands from running.
func declaredTests(cfg testsConfig, staged string) ([]testDeclaration, workflow.StepState, string) {
	contract, err := docker.ReadWorkspaceContract(staged)
	if err != nil && !errors.Is(err, docker.ErrNoContract) {
		return nil, workflow.StatusError, err.Error()
	}
	var declarations []testDeclaration
	declared := map[string]bool{}
	if contract != nil {
		for _, part := range contract.Parts {
			declared[part.Name] = len(part.Test) > 0
			for _, command := range part.Test {
				declarations = append(declarations, testDeclaration{part.Name, part.Folder(), command})
			}
		}
	}
	var missing []string
	why := "no tests are declared in .apptown/app.yaml"
	if len(cfg.AppliesTo) > 0 {
		src := verification.CheckSource(staged)
		places, explanation, err := check.Applies(cfg.AppliesTo, src)
		if err != nil {
			return nil, workflow.StatusError, err.Error()
		}
		why += "; " + explanation
		for _, place := range places {
			for _, name := range place.Parts {
				if declared[name] {
					continue
				}
				var tools []string
				for _, part := range src.Parts {
					if part.Name != name {
						continue
					}
					for _, tool := range part.Tools {
						if slices.Contains(check.AppliesTools(cfg.AppliesTo), tool) {
							tools = append(tools, tool)
						}
					}
				}
				description := "declares no tests"
				if len(tools) > 0 {
					description = "is built with " + strings.Join(tools, " and ") + " and " + description
				}
				missing = append(missing, name+" "+description+": add `test:` to the part in .apptown/app.yaml")
			}
		}
	}
	if len(missing) > 0 {
		return declarations, workflow.StatusFail, strings.Join(missing, "; ")
	}
	if len(declarations) > 0 {
		return declarations, workflow.StatusPass, ""
	}
	if cfg.Require {
		return nil, workflow.StatusFail, "this workflow requires tests, and none are declared: add `test:` to a part in .apptown/app.yaml"
	}
	return nil, workflow.StatusNotApplicable, why
}
