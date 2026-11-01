package pi

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Labels answers the agent image's label of that name, "" when it has none.
// What a session is promised (subagents, extensions) is read from the
// labels of the image it runs in: AppTown's own Docker's agent image for
// work here (Preparer.Labels), the worker's for work on a worker
// (MapLabels over what the worker reports).
type Labels func(name string) string

// MapLabels answers labels from a map, as a worker reports them.
func MapLabels(labels map[string]string) Labels {
	return func(name string) string { return labels[name] }
}

// SubagentsLabel carries the version of the baked subagent extension.
const SubagentsLabel = dockerlabel.PiSubagentsVersion

func subagentsVersion(labels Labels) string {
	return labels(SubagentsLabel)
}

// labelsFor reads AppTown's own Docker's agent image, or, given WithLabels,
// the image read there.
func (home Preparer) labelsFor(ctx context.Context) Labels {
	if home.labels != nil {
		return home.labels()
	}
	return func(name string) string { return home.docker.AgentImageLabel(ctx, home.image, name) }
}

// WithLabels reads the agent image's labels from labels instead of the
// AppTown's own Docker: on an AppTown with none, from the worker.
func (home Preparer) WithLabels(labels func() Labels) Preparer {
	home.labels = labels
	return home
}

// Delegation is DelegationWith AppTown's own agent image's labels.
func (home Preparer) Delegation(ctx context.Context) string {
	return DelegationWith(home.labelsFor(ctx))
}

// DelegationWith: the steering section that promises the subagent tool
// — only when the image actually carries it (steering promises are
// guarantees; a missing tool is a platform defect the agent will report).
// Empty when there is nothing to promise. Both console kinds include it.
func DelegationWith(labels Labels) string {
	if subagentsVersion(labels) == "" {
		return ""
	}
	return "## Delegating (subagent tool)\n" + `
Two helper agents are available through the subagent tool, running on
cheaper models than this session:
- scout — fast read-only lookups: find files, trace behavior, summarize.
  Prefer it for searches so your own context stays clean.
- worker — carries out a written brief. It cannot see this conversation:
  the brief must contain everything it needs, including file paths.
Do the thinking and deciding yourself; delegate the legwork. Subagent
transcripts are recorded with this session.

If the repo has .agents/skills/, those skills load on demand when their
topic comes up.
`
}

// configuration supplies only credential-checked public configuration.
// Home preparation cannot read provider credentials or private upstream URLs.
type configuration interface {
	ModelsJSON(context.Context) ([]byte, error)
}

type Preparer struct {
	models     configuration
	selection  modelSlots
	extensions extensionSelection
	docker     *docker.Scope
	image      string
	labels     func() Labels
}

// The adapter can read model and extension selections, but cannot change them
// or settle work. The owners keep fallback and project-policy rules.
type modelSlots interface {
	Resolve(ctx context.Context, projectID ids.ID, requested string) (pattern, slot, profile string, err error)
}
type extensionSelection interface {
	EnabledExtensions(ctx context.Context, projectID ids.ID) ([]string, error)
}

func NewPreparer(models configuration, selection modelSlots, extensions extensionSelection, scope *docker.Scope, image string) Preparer {
	return Preparer{models: models, selection: selection, extensions: extensions, docker: scope, image: image}
}

// Stage generates pi's provider config from AppTown's model
// bindings — no host ~/.pi state is copied into containers. Custom providers
// (e.g. a local LM Studio endpoint) live in binding config. The runtime file
// contains only the proxy URL, public model metadata and a session-token
// environment reference. The generated file is
// 0600 inside the workspace pi-home (gitignored, excluded from harvest).
func (home Preparer) Stage(ctx context.Context, ws string, projectID ids.ID) (string, error) {
	dst := filepath.Join(ws, ".apptown", "pi-home", ".pi", "agent")
	if err := os.RemoveAll(filepath.Join(ws, ".apptown", "pi-home")); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	models, err := home.models.ModelsJSON(ctx)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dst, "models.json"), models, 0o600); err != nil {
		return "", err
	}
	// Settings + delegation surface (pihome.go): trust closed, blessed repo
	// skills path, the baked extension, worker/scout on this project's slots.
	if err := home.StageAgentDir(ctx, dst, projectID); err != nil {
		return "", err
	}
	return filepath.Join(ws, ".apptown", "pi-home"), nil
}

// StageAgentDir resolves the project's selected slots and extensions into
// Pi files, for AppTown's own agent image.
func (home Preparer) StageAgentDir(ctx context.Context, agentDir string, projectID ids.ID) error {
	config, _, extErr, err := home.agentHome(ctx, projectID, home.labelsFor(ctx))
	if err != nil {
		return err
	}
	if _, err := database.Lookup(extErr); err != nil {
		log.Printf("read failed: %v", err)
	}
	return stageAgentDir(agentDir, config)
}

// agentHome is what the project selected, as the image's labels provide
// it, the enabled extensions the image does not carry, and a failure to
// read which extensions are enabled (extErr, with the home as if none were).
// err is a failure to resolve the delegates' models: no home is staged on a
// model nobody selected.
func (home Preparer) agentHome(ctx context.Context, projectID ids.ID, labels Labels) (config preparedHome, missing []string, extErr, err error) {
	config.SubagentsVersion = subagentsVersion(labels)
	if config.SubagentsVersion != "" {
		if config.WorkerModel, _, _, err = home.selection.Resolve(ctx, projectID, "worker"); err != nil {
			return preparedHome{}, nil, nil, fmt.Errorf("choosing the worker delegate's model: %w", err)
		}
		if config.ScoutModel, _, _, err = home.selection.Resolve(ctx, projectID, "scout"); err != nil {
			return preparedHome{}, nil, nil, fmt.Errorf("choosing the scout delegate's model: %w", err)
		}
	}
	config.Packages, missing, extErr = home.enabledExtensionPackages(ctx, projectID, labels)
	return config, missing, extErr, nil
}

// enabledExtensionPackages resolves opt-ins only to baked, curated
// packages, and names the enabled ones the image's labels do not carry.
func (home Preparer) enabledExtensionPackages(ctx context.Context, projectID ids.ID, labels Labels) (packages, missing []string, err error) {
	names, err := home.extensions.EnabledExtensions(ctx, projectID)
	for _, name := range names {
		ext := optionalExtension(name)
		if ext == nil {
			continue
		}
		if version := labels(ext.Label); version != "" {
			packages = append(packages, "npm:"+ext.Package+"@"+version)
		} else {
			missing = append(missing, name)
		}
	}
	return packages, missing, err
}

// sentHome is Pi's generated home as files to send, for a session whose
// home is not written by AppTown (a step on a worker): paths relative to
// HOME, regular files only. Link is the one link the home needs, which
// whoever places the files makes itself; Missing names the extensions the
// project enabled that the image's labels do not carry.
type sentHome struct {
	Files   []sentFile
	Link    SentLink
	Missing []string
}
type sentFile struct {
	Path    string // relative to HOME, slash-separated
	Content []byte
}

// SentLink is a symbolic link at Path (relative to HOME) to Target; zero
// when the home needs none.
type SentLink struct{ Path, Target string }

// SentHome generates the home Stage writes, as files, from the labels of
// the image the session runs in. Nothing is written anywhere but a
// temporary folder of its own. Extensions that cannot be read are an
// error, never "none enabled".
func (home Preparer) SentHome(ctx context.Context, projectID ids.ID, labels Labels) (sentHome, error) {
	config, missing, extErr, err := home.agentHome(ctx, projectID, labels)
	if err != nil {
		return sentHome{}, err
	}
	if _, err := database.Lookup(extErr); err != nil {
		return sentHome{}, fmt.Errorf("reading the project's extensions: %w", err)
	}
	dir, err := os.MkdirTemp("", "apptown-pi-home-")
	if err != nil {
		return sentHome{}, err
	}
	defer os.RemoveAll(dir)
	piAgent := filepath.Join(dir, ".pi", "agent")
	if err := os.MkdirAll(piAgent, 0o755); err != nil {
		return sentHome{}, err
	}
	models, err := home.models.ModelsJSON(ctx)
	if err != nil {
		return sentHome{}, fmt.Errorf("generating pi config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(piAgent, "models.json"), models, 0o600); err != nil {
		return sentHome{}, err
	}
	if err := stageAgentDir(piAgent, config); err != nil {
		return sentHome{}, err
	}
	staged, err := stagedUnder(dir, "")
	if err != nil {
		return sentHome{}, err
	}
	out := sentHome{Missing: missing}
	names := make([]string, 0, len(staged))
	for name := range staged {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := staged[name]
		switch {
		case f.Link != "" && out.Link == SentLink{}:
			out.Link = SentLink{Path: filepath.ToSlash(name), Target: f.Link}
		case f.Link != "":
			return sentHome{}, fmt.Errorf("pi's home has more than one link: %s", name)
		default:
			out.Files = append(out.Files, sentFile{Path: filepath.ToSlash(name), Content: f.Data})
		}
	}
	return out, nil
}
