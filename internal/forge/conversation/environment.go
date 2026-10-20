package conversation

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// Environment prepares a launch without owning session lifetime or provider keys.
// The console runtime calls it again on restart, so model and service facts are live.
type Environment struct {
	db           *database.DB
	projects     *project.Projects
	models       model.Selection
	bindings     *integration.Bindings
	instructions *agentenv.Instructions
	home         consoleHome
	cli          executable
	credentials  credentialCheck
	identity     *identity.Service
	cfg          EnvironmentConfig
	clock        func() time.Time
}
type consoleHome interface {
	ConsoleFiles(context.Context, string, ids.ID) (map[string]console.Staged, error)
}
type executable interface{ Path() (string, error) }
type credentialCheck interface {
	CheckEnvironment(context.Context, []string) error
}
type EnvironmentDependencies struct {
	Projects     *project.Projects
	Models       model.Selection
	Bindings     *integration.Bindings
	Instructions *agentenv.Instructions
	Home         consoleHome
	CLI          executable
	Credentials  credentialCheck
	Identity     *identity.Service
	// Now is the forge's clock, which stamps the environment's receipts
	// (the real clock when nil).
	Now func() time.Time
}
type EnvironmentConfig struct {
	DataDir, APIURL, DefaultModel string
	InferenceEnabled              bool
	NoPrivateDocker               bool
}

func NewEnvironment(db *database.DB, deps EnvironmentDependencies, cfg EnvironmentConfig) *Environment {
	return &Environment{db: db, projects: deps.Projects, models: deps.Models, bindings: deps.Bindings, instructions: deps.Instructions, home: deps.Home, cli: deps.CLI, credentials: deps.Credentials, identity: deps.Identity, cfg: cfg, clock: deps.Now}
}
func (e *Environment) Prepare(ctx context.Context, s *console.Session) (console.Provision, error) {
	if err := agentenv.RequireInferenceRuntime(e.cfg.InferenceEnabled, execution.Docker); err != nil {
		return console.Provision{}, err
	}
	// A chat on a closed change is kept as a transcript; it never runs
	// again, so nothing writes the folder of a change that has closed.
	if changeID := Of(s).ChangeID; !changeID.IsZero() {
		ch, err := workflow.ReadChangeIn(ctx, e.db, changeID)
		if err != nil {
			return console.Provision{}, err
		}
		if ch.State != workflow.ChangeInProgress {
			return console.Provision{}, fmt.Errorf("change #%d is %s: its chat is kept to read, and cannot run again", ch.ID, ch.State)
		}
		// Only the chat that holds its change runs: one that does not may
		// share the folder with the change's worker.
		authority, err := scm.ReadAuthorityIn(ctx, e.db, ch.ProjectID, ch.Branch)
		if err != nil {
			return console.Provision{}, err
		}
		if authority.Holder != scm.ChatHolder(s.ID) {
			return console.Provision{}, fmt.Errorf("this chat does not hold change #%d (%s does); open the change's chat again", ch.ID, authority.Holder)
		}
	}
	// An unpinned session follows the project's chat slot: the model it was
	// born with is not a promise, the slot is.
	model := s.Model
	if want := e.chatModelFor(ctx, s); want != "" && want != model {
		audit.At(clock.Or(e.clock)()).BestEffort(ctx, e.db, s.Actor, Of(s).ProjectID, "console.model-followed", s.ID, map[string]any{"from": model, "to": want})
		model = want
	}
	// A model may name a binding that no longer exists (renamed since the
	// session was created): fall back to the instance default rather than
	// resuming into a dead provider.
	if model != "" {
		prefix := model
		if i := strings.IndexByte(prefix, '/'); i > 0 {
			prefix = prefix[:i]
		}
		n, err := e.bindings.Count(ctx, prefix, "model")
		if err != nil {
			return console.Provision{}, err
		}
		if n == 0 {
			model = e.cfg.DefaultModel
		}
	}

	files, err := e.home.ConsoleFiles(ctx, e.cfg.DataDir, Of(s).ProjectID)
	if err != nil {
		return console.Provision{}, err
	}
	// What the prompt says about the provision: the registries and
	// integrations the agent can reach, and a change console's Docker.
	notes := e.instructions.Registry(ctx) + e.instructions.Integrations(ctx, Of(s).ProjectID)
	if !Of(s).ChangeID.IsZero() {
		notes += "\nConsole-specific notes on Docker:\n" + agentenv.ConsoleDockerInstructions(e.cfg.NoPrivateDocker) + "\n"
	}
	if OnWorkerRecord(s.Meta) {
		notes += workerNotes
	}

	// The forge the agent talks back to, and who it is there: the read-only
	// listener, the session's token as its credential, and its change.
	env := []string{"APPTOWN_URL=" + e.cfg.APIURL, "APPTOWN_USER=" + s.Actor + " via console/" + s.ID,
		"APPTOWN_SESSION_ID=" + s.ID, "APPTOWN_SESSION_TOKEN=" + s.Token}
	if changeID := Of(s).ChangeID; !changeID.IsZero() {
		// `apptown check` and friends infer the change from the session.
		env = append(env, fmt.Sprintf("APPTOWN_CHANGE=%d", changeID))
	}
	// The scoped git remote: fetch main, read-only, this project only.
	if p, err := e.projects.BySlug(ctx, Of(s).Slug); err == nil {
		remote, err := e.gitRemote(ctx, p)
		if err != nil {
			return console.Provision{}, fmt.Errorf("the chat's git remote: %w", err)
		}
		env = append(env, remote...)
	}
	var mounts []string
	// Ambient build environment: registry routing and mise, so interactive
	// sessions build exactly like steps do.
	// Its files are written into the session's own folder (never a
	// project's tree), afresh for each start, and mounted from there.
	amb := agentenv.AmbientFor(integration.RegistryBindingsIn(ctx, e.db), s.Dir)
	dir := filepath.Join(s.Dir, ".forge", "ambient")
	if err := os.RemoveAll(dir); err != nil {
		return console.Provision{}, fmt.Errorf("the chat's generated configuration: %w", err)
	}
	ambMounts, err := execution.StageFiles(dir, amb.Files)
	if err != nil {
		return console.Provision{}, fmt.Errorf("the chat's generated configuration: %w", err)
	}
	env = append(env, amb.Env...)
	mounts = append(mounts, ambMounts...)
	if cli, err := e.cli.Path(); err == nil {
		mounts = append(mounts, cli+":"+agentenv.CLIDir+"/apptown:ro")
		env = agentenv.WithCLIPath(env)
	}
	if err := e.credentials.CheckEnvironment(ctx, env); err != nil {
		return console.Provision{}, err
	}
	return console.Provision{Model: model, Env: env, Mounts: mounts, Files: files, Notes: notes}, nil
}

// chatModelFor is the model a session should run on now: its own when the
// person pinned one, else the project's chat slot (falling back to the
// instance default) as resolved at this moment.
func (e *Environment) chatModelFor(ctx context.Context, s *console.Session) string {
	if s.ModelPinned {
		return s.Model
	}
	m, _, _ := e.models.Resolve(ctx, Of(s).ProjectID, "chat")
	if m == "" {
		m, _, _ = e.models.Resolve(ctx, ids.Nil, "")
	}
	return m
}

// gitRemote is the environment of the project's read-only fetch endpoint:
// APPTOWN_MAIN_REMOTE, a plain URL, and git configuration that sends the pull
// token to that URL only. The token is never part of the URL, so a remote,
// an error message or a pasted command does not carry it.
func (e *Environment) gitRemote(ctx context.Context, p project.Project) ([]string, error) {
	if e.cfg.APIURL == "" {
		return nil, nil
	}
	token, err := e.identity.RuntimePullToken(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return mainRemote(e.cfg.APIURL, token, p.Slug), nil
}

// mainRemote is the project's Git URL under the forge's address, for http
// and https alike, with the token as a header git sends only to it.
func mainRemote(base, token, slug string) []string {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	remote := strings.TrimRight(u.String(), "/") + "/git/" + slug
	return []string{"APPTOWN_MAIN_REMOTE=" + remote, "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http." + remote + ".extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Bearer " + token}
}

// workerNotes tell a chat on the worker what differs there
// (design/operations/workers.md, "Chats on the worker").
const workerNotes = `
This chat runs on a separate machine, the worker. Your work reaches the change
when it is saved: after each turn AppTown collects your commits, checks them
as it checks any publication, and puts them on the change's branch. Only
committed work is saved, so commit as you go. If a save is refused you are told
why, with how to fix it. ` + "`apptown check`" + ` saves your commits first and checks what
was saved, so commit before asking. Previews work the same way: ` + "`apptown preview`" + `
saves first and boots what was saved, and after each saved turn AppTown
boots your saved commit like the try-it button and tells you if it fails.
`
