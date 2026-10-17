package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/dockerlabel"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/tasks"

	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/strs"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/execution"

	piharness "flokkit.io/forge/internal/forge/agentharness/pi"

	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/integration"

	"flokkit.io/forge/internal/forge/workflow"
)

// --- running pi ------------------------------------------------------------

// piCall is one invocation of pi on behalf of a step.
type piCall struct {
	Source    workflow.Source
	Step      *workflow.Step
	Executor  string
	Image     string
	Workspace string
	Args      []string // pi's argv after "pi"
	Env       []string
	Network   string
	Timeout   time.Duration
	Name      string // container name; a retry gets "-r2" appended
	// RetryEmpty: one automatic re-run when the model returns an empty turn
	// (local models occasionally do, 0 output tokens) — never when the
	// provider refused, timed out, or the process failed.
	RetryEmpty bool
	// Session, when set, is the admitted session whose transcript is kept
	// after each invocation (transcript.go).
	Session string
}

// piResult is what came back: the raw outcome plus the parsed transcript.
type piResult struct {
	execution.Outcome
	Text     string // all assistant text, joined
	Model    string
	Provider string
	Usage    map[string]any
	// ProviderError: the provider's own words when the turn ended in an
	// error and produced no text (billing, auth, quota) — infrastructure,
	// not the agent's work.
	ProviderError string
	Tries         int
	// UsageIncomplete says why the usage recorded for one of these
	// invocations may be lower than what was spent (its transcript could
	// not be read whole); "" when every invocation's was complete.
	UsageIncomplete string
}

// piInvoke runs pi once (twice on an empty turn when RetryEmpty) through the
// executor. Every invocation carries the ambient build environment and the
// forge CLI in its own PATH-first dir (agentenv.CLIDir) — by construction, so no
// caller can forget them (a resumed review once lost the CLI it was given to
// verify claims), and nothing can shadow it. Streams the transcript to the live
// feed, records each invocation's token usage.
func (a *Executor) piInvoke(ctx context.Context, c piCall) piResult {
	var r piResult
	usageIncomplete := ""
	for tries := 1; ; tries++ {
		amb := agentenv.AmbientFor(integration.RegistryBindingsIn(ctx, a.db), c.Workspace)
		var mounts []string
		var files []execution.File
		if c.Executor == execution.Docker {
			files = amb.Files // written beside the step's folder, never in it
		}
		env := append(append([]string{}, c.Env...), amb.Env...)
		if cli, err := a.cli.Path(); err == nil {
			mounts = append(mounts, cli+":"+agentenv.CLIDir+"/forge:ro")
			env = agentenv.WithCLIPath(env)
		}
		name := c.Name
		if tries > 1 {
			name += fmt.Sprintf("-r%d", tries)
		}
		argv := console.ToolchainArgs(append([]string{"pi"}, c.Args...))
		// Every invocation of a step, whether a retry, a review's wrap-up or
		// a re-run after an empty turn, waits until no earlier agent of the
		// step is still there: they would share the step's folder.
		inContainer := c.Executor == execution.Docker
		if inContainer {
			if err := a.stepAgentsGone(ctx, c.Step.ID, dockerlabel.Class+"=step"); err != nil {
				r = piResult{Outcome: execution.Outcome{RunErr: err}, Tries: tries}
				break
			}
		}
		out := a.process.Run(ctx, c.Executor, execution.Request{
			Workspace: c.Workspace,
			Argv:      argv,
			Env:       env,
			Mounts:    mounts,
			Files:     files,
			Timeout:   c.Timeout,
			Image:     c.Image,
			Network:   c.Network,
			Name:      name,
			Labels:    []string{docker.WritesLabel + "=" + c.Workspace, dockerlabel.Step + "=" + c.Step.ID.String()},
			OnLine: func(line string) {
				if s := piharness.SummarizeEvent(line); len(s) > 0 {
					a.progress.Append(c.Step.ID, s...)
				}
			},
		})
		r = piResult{Outcome: out, Tries: tries}
		var historyErr error
		if c.Session != "" {
			// Kept before anything else runs in the session: a later
			// invocation starts only after this one's history is stored
			// or recorded unavailable.
			historyErr = a.keepTranscript(ctx, c, tries)
		}
		r.Text, r.Model, r.Provider, r.Usage = piharness.ParseOutput(out.Output)
		if incomplete := a.recordInvocationUsage(ctx, c, name, r); incomplete != "" && usageIncomplete == "" {
			usageIncomplete = name + ": " + incomplete
		}
		if historyErr != nil {
			// A history neither recorded nor recorded unavailable is an
			// error of the step: nothing more runs in the session, and the
			// attempt is never read later as one that kept nothing.
			r.RunErr = errors.Join(r.RunErr, historyErr)
			break
		}
		// A provider that refused the request (billing, auth, quota) leaves
		// an empty turn with an error on it. That is not "the agent produced
		// nothing" and retrying does not help — say what the provider said.
		if perr := piharness.LastModelError(out.Output); perr != "" && strings.TrimSpace(r.Text) == "" {
			r.ProviderError = perr
			a.progress.Append(c.Step.ID, "✗ the model provider refused: "+strs.Truncate(perr, 200))
			break
		}
		if c.RetryEmpty && strings.TrimSpace(r.Text) == "" && !out.TimedOut && out.RunErr == nil && tries < 2 {
			recordEvent(ctx, a.db, c.Source.RunID, "step.retried", map[string]any{"step": c.Step.Name, "reason": "empty model output"}, a.now())
			a.progress.Append(c.Step.ID, "⟳ empty model output — retrying")
			continue
		}
		break
	}
	r.UsageIncomplete = usageIncomplete
	return r
}

// recordInvocationUsage records what one invocation of pi spent, once: each
// try of an empty-turn retry and each review wrap-up is its own invocation,
// keyed by the step, its attempt and the invocation's container name, so
// every one counts and a replay of the same invocation counts nothing more.
// A transcript that could not be read whole is recorded as incomplete, even
// when no usage survived, and the reason is returned for the evidence.
func (a *Executor) recordInvocationUsage(ctx context.Context, c piCall, name string, r piResult) string {
	spent := sumPiUsage(r.Output)
	if spent.Totals.Total == 0 && spent.Incomplete == "" {
		return ""
	}
	um, up := spent.Model, spent.Provider
	if um == "" {
		um, up = r.Model, r.Provider
	}
	source := c.Source
	key := fmt.Sprintf("step:%d:a%d:%s", c.Step.ID, c.Step.Attempt, name)
	if err := a.ledger.RecordOnce(ctx, key, usage.Entry{Kind: "step", ProjectID: source.ProjectID, RunID: source.RunID, RootRunID: source.RootRunID, StepID: c.Step.ID, WorkflowName: source.WorkflowName, WorkflowVersion: int64(source.WorkflowVersion), Provider: up, Model: um}, spent.Totals, spent.Incomplete); err != nil {
		log.Printf("recording step %d usage (%s): %v", c.Step.ID, name, err)
	}
	return spent.Incomplete
}

// stepAgentsGone removes what is left of a step's earlier containers (those
// labelled with the step, and with filters when given) and fails unless
// Docker confirms none remains.
func (a *Executor) stepAgentsGone(ctx context.Context, stepID ids.ID, filters ...string) error {
	if a.docker == nil {
		return fmt.Errorf("no Docker to confirm that step %d's earlier agents have stopped", stepID)
	}
	left, err := a.docker.RemoveLabelled(ctx, append([]string{dockerlabel.Step + "=" + stepID.String()}, filters...)...)
	if err != nil {
		return fmt.Errorf("an earlier agent of this step may still be running: %w", err)
	}
	if len(left) > 0 {
		return fmt.Errorf("an earlier agent of this step is still running and could not be removed (%s); retry once it is gone", strings.Join(left, ", "))
	}
	return nil
}

// earlierTasksStopped refuses an attempt of step while another script task
// of the run's tree has not settled: it may still write this folder. There
// is no exception for a task whose step is at work: its stop could fail
// while this attempt waits to start, and ordinary dispatch runs a stage's
// scripts one at a time anyway. The attempt's own task is excluded, and a
// script checks again after reserving its own (runScriptTask), so two
// admitted at once each see the other and neither starts. The other task
// stays unsettled until its outcome or stop is resolved; nothing here
// settles it. A task given a source commit ran on a worker's own checkout
// and never wrote this folder.
func earlierTasksStopped(ctx context.Context, db database.Queryer, run *workflow.Run, step *workflow.Step) error {
	unsettled, err := tasks.UnsettledIn(ctx, db)
	if err != nil || len(unsettled) == 0 {
		return err
	}
	tree, err := workflow.RunTreeIn(ctx, db, run)
	if err != nil {
		return err
	}
	for _, t := range unsettled {
		if t.Source != "" || !slices.Contains(tree, t.Owner.RunID) {
			continue
		}
		if t.Owner.StepID == step.ID && t.Owner.Attempt == step.OwnedAttempt() {
			continue // this attempt's own task
		}
		return fmt.Errorf("another script task (%s) has not settled and may still write this folder; retry once it has", t.Operation)
	}
	return nil
}

// folderReady removes what is left writing ws from before this attempt of
// step (an earlier attempt, its private Docker daemon, another step's
// container that outlived it) and fails unless Docker then lists nothing. That
// covers an earlier attempt of this step, its private Docker daemon, and a
// sibling step whose container outlived it. It fails unless Docker then
// lists nothing but the running steps' containers.
func (a *Executor) folderReady(ctx context.Context, stepID ids.ID, ws string) error {
	if a.docker == nil {
		return fmt.Errorf("no Docker to confirm that nothing from before is still writing %s", ws)
	}
	return folderReady(ctx, a.docker, stepID, ws, "")
}

func folderReady(ctx context.Context, dk *docker.Scope, stepID ids.ID, ws, own string) error {
	if dk.Absent() {
		// A forge with no local Docker runs nothing that writes the folder
		// here, and started only once nothing local was left
		// (-no-local-docker's precondition). Remote work is fenced by its
		// task and holder records, not here.
		return nil
	}
	// Under the folder's lock no other step can be working here, so every
	// container still labelled as writing the folder is a leftover, however
	// its step is recorded (a step whose outcome could not be written stays
	// marked running). Only a recovered task's own container, named for its
	// operation, is kept.
	stale := func() ([]string, error) {
		writers, err := dk.ListLabelled(ctx, dockerlabel.Step, docker.WritesLabel+"="+ws)
		if err != nil {
			return nil, fmt.Errorf("something from before may still be writing this step's folder: %w", err)
		}
		var names []string
		for name := range writers {
			if own != "" && strings.HasSuffix(name, own) {
				continue
			}
			names = append(names, name)
		}
		return names, nil
	}
	names, err := stale()
	if err != nil || len(names) == 0 {
		return err
	}
	for _, name := range names {
		if out, err := dk.Run(ctx, "rm", "-f", name); err != nil {
			log.Printf("step %d: removing %s, left writing its folder: %v: %s", stepID, name, err, out)
		}
	}
	if names, err = stale(); err != nil {
		return err
	}
	if len(names) > 0 {
		return fmt.Errorf("something from before is still writing this step's folder and could not be removed (%s); retry once it is gone", strings.Join(names, ", "))
	}
	return nil
}
