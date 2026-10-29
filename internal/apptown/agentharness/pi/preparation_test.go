package pi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type publicConfig struct{ err error }

func (c publicConfig) ModelsJSON(context.Context) ([]byte, error) {
	return []byte(`{"providers":{}}`), c.err
}

// selectedSlots resolves every slot, except that failing (when set) fails
// with err.
type selectedSlots struct {
	failing string
	err     error
}

func (s selectedSlots) Resolve(ctx context.Context, _ ids.ID, slot string) (string, string, string, error) {
	if slot == s.failing {
		return "", "", "", s.err
	}
	return "test/" + slot, slot, "standard", nil
}

type selectedExtensions struct{}

func (selectedExtensions) EnabledExtensions(context.Context, ids.ID) ([]string, error) {
	return []string{"lsp", "not-curated"}, nil
}

type bakedLabels struct{}

func (bakedLabels) Run(_ context.Context, _ string, command docker.Command) ([]byte, error) {
	args := strings.Join(command.Args, " ")
	if strings.HasPrefix(args, "image inspect") {
		return []byte(`"sha256:` + strings.Repeat("b", 64) + `" {"apptown.pi-subagents.version":"1.0","apptown.pi-lsp.version":"2.0"}`), nil
	}
	return nil, errors.New("unexpected image query")
}

// Staging for a persistent console must preserve the private provider-file
// permissions and baked-package symlink when it becomes a set of launch files.
func TestConsoleFilesPreservePrivateModesAndBakedLinks(t *testing.T) {
	t.Parallel()
	scope := docker.Scope{Client: bakedLabels{}}
	home := NewPreparer(publicConfig{}, selectedSlots{}, selectedExtensions{}, &scope, "test-agent")
	files, err := home.ConsoleFiles(t.Context(), t.TempDir(), idstest.N(7))
	if err != nil {
		t.Fatal(err)
	}
	models := files["pi-home/.pi/agent/models.json"]
	if models.Mode != 0600 || string(models.Data) != `{"providers":{}}` {
		t.Fatalf("provider file: %+v", models)
	}
	if files["pi-home/.pi/agent/npm"].Link != "/opt/pi-baked/.pi/agent/npm" {
		t.Fatal("baked package link was lost")
	}
	settings := string(files["pi-home/.pi/agent/settings.json"].Data)
	if !strings.Contains(settings, "npm:@narumitw/pi-lsp@2.0") || strings.Contains(settings, "not-curated") {
		t.Fatalf("extension selection: %s", settings)
	}
	if !strings.Contains(string(files["pi-home/.pi/agent/agents/worker.md"].Data), "model: test/worker") {
		t.Fatal("worker slot was lost")
	}
	refusal := errors.New("configuration refused")
	home.models = publicConfig{err: refusal}
	if files, err := home.ConsoleFiles(t.Context(), t.TempDir(), idstest.N(7)); !errors.Is(err, refusal) || files != nil {
		t.Fatalf("configuration refusal: %v %v", files, err)
	}
}

// A delegate's model that cannot be resolved stages no home: neither a
// console's files, a workspace's pi home nor a worker's sent home is written
// with the delegate on no model or another one.
func TestAnUnresolvedDelegateModelStagesNoHome(t *testing.T) {
	t.Parallel()
	scope := docker.Scope{Client: bakedLabels{}}
	unread := errors.New("model slots unreadable")
	for _, delegate := range []string{"worker", "scout"} {
		home := NewPreparer(publicConfig{}, selectedSlots{failing: delegate, err: unread}, selectedExtensions{}, &scope, "test-agent")
		if files, err := home.ConsoleFiles(t.Context(), t.TempDir(), idstest.N(7)); !errors.Is(err, unread) || files != nil {
			t.Fatalf("%s: console files: %v %v", delegate, files, err)
		}
		if _, err := home.Stage(t.Context(), t.TempDir(), idstest.N(7)); !errors.Is(err, unread) {
			t.Fatalf("%s: workspace home: %v", delegate, err)
		}
		if _, err := home.SentHome(t.Context(), idstest.N(7), home.Labels(t.Context())); !errors.Is(err, unread) {
			t.Fatalf("%s: sent home: %v", delegate, err)
		}
	}
}
