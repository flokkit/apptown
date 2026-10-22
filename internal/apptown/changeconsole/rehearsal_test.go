package changeconsole

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"io/fs"
	"maps"
	"net/http/httputil"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
)

const staticSiteContract = "version: 1\napp:\n  web:\n    static: .\n    public: true\n"

// workerPreviews is a worker's previews as Deployment asks them
// (preview.Worker): it records each boot with the tree it was
// sent, answers each preview as running unless answer says otherwise, and
// refuses everything while down.
type workerPreviews struct {
	mu      sync.Mutex
	boots   []previewBoot
	running map[string]worker.PreviewState
	down    bool
	refused int                                               // requests refused while down
	answer  func(spec worker.PreviewSpec) worker.PreviewState // nil: running, as asked
}

type previewBoot struct {
	spec  worker.PreviewSpec
	files map[string]string
}

var errWorkerDown = errors.New("the worker is down")

func (f *workerPreviews) refuse() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		f.refused++
	}
	return f.down
}

func (f *workerPreviews) Boot(_ context.Context, spec worker.PreviewSpec, _ docker.Prepared, tree string) (worker.PreviewState, error) {
	if f.refuse() {
		return worker.PreviewState{}, errWorkerDown
	}
	files := map[string]string{}
	_ = filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			body, _ := os.ReadFile(path)
			rel, _ := filepath.Rel(tree, path)
			files[filepath.ToSlash(rel)] = string(body)
		}
		return nil
	})
	f.mu.Lock()
	f.boots = append(f.boots, previewBoot{spec: spec, files: files})
	answer := f.answer
	f.mu.Unlock()
	st := worker.PreviewState{Running: true, Incarnation: spec.Incarnation, ContainerID: "c-" + spec.Name}
	if answer != nil {
		st = answer(spec)
	}
	if st.Running {
		f.mu.Lock()
		if f.running == nil {
			f.running = map[string]worker.PreviewState{}
		}
		f.running[spec.Name] = st
		f.mu.Unlock()
	}
	return st, nil
}

func (f *workerPreviews) State(_ context.Context, name string) (worker.PreviewState, error) {
	if f.refuse() {
		return worker.PreviewState{}, errWorkerDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[name], nil
}

func (f *workerPreviews) Remove(_ context.Context, name string) error {
	if f.refuse() {
		return errWorkerDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		f.running = nil
		return nil
	}
	delete(f.running, name)
	return nil
}

func (f *workerPreviews) List(context.Context) ([]string, error) {
	if f.refuse() {
		return nil, errWorkerDown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.running)), nil
}

func (f *workerPreviews) RemoveImages(context.Context, string) error         { return nil }
func (f *workerPreviews) Admit(context.Context, *appcontract.Contract) error { return nil }
func (f *workerPreviews) Forward(string, string) *httputil.ReverseProxy      { return nil }

func (f *workerPreviews) bootsNamed(name string) []previewBoot {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []previewBoot
	for _, b := range f.boots {
		if b.spec.Name == name {
			out = append(out, b)
		}
	}
	return out
}

func (f *workerPreviews) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// rehearsalWorld is a chat on the worker holding its change, with previews
// on the worker too (and a preview domain), and a commit on the worker
// declaring a static site.
func rehearsalWorld(t *testing.T) (world, *workerPreviews, *console.Session) {
	t.Helper()
	f := &workerPreviews{}
	w := newWorldWith(t, f)
	s := w.warmOnWorker(t)
	w.commitOnWorker(t, s.ID, ".apptown/app.yaml", staticSiteContract)
	return w, f, s
}

// rehearsed is the saved commit a chat's last rehearsal booted: the source
// part of the incarnation its marker holds.
func rehearsed(s *console.Session) string {
	b, _ := os.ReadFile(filepath.Join(s.Dir, console.RehearsedFile))
	commit, _, _ := strings.Cut(strings.TrimSpace(string(b)), "+")
	return commit
}

func toldTheChat(s *console.Session, text string) bool {
	for _, m := range s.Messages() {
		if strings.Contains(m.Text, text) {
			return true
		}
	}
	return false
}

// saveWithoutRehearsal saves the chat's commits without queueing a
// rehearsal of its own: the test rehearses.
func saveWithoutRehearsal(t *testing.T, w world, s *console.Session) string {
	t.Helper()
	saved, err := w.saves.SaveAndReport(context.Background(), scm.ChatHolder(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// A turn's save that succeeds rehearses the saved commit on the worker:
// its tree, exactly as saved, boots there as the try-it button would, and
// the commit is then rehearsed.
func TestASavedTurnIsRehearsedOnTheWorker(t *testing.T) {
	t.Parallel()
	w, fake, s := rehearsalWorld(t)
	w.chats.SaveAfterTurn(s.ID)
	saved := w.branchTip()
	name := docker.PreviewChangeName(w.change.ID)
	eventually(t, "the saved commit booted on the worker", func() bool { return len(fake.bootsNamed(name)) == 1 })
	if b := fake.bootsNamed(name)[0]; b.files[".apptown/app.yaml"] != staticSiteContract {
		t.Fatalf("the rehearsal booted another tree: %+v", b.files)
	}
	eventually(t, "the commit marked rehearsed", func() bool { return rehearsed(s) == saved })
}

// A saved commit is rehearsed once. A worker that cannot be asked is not
// the app's failure: the chat is told nothing, and the commit stays to be
// rehearsed. A boot that fails is the app's: the chat is told, with the
// saved commit, and the commit is marked.
func TestTheRehearsalOfASavedCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, fake, s := rehearsalWorld(t)
	name := docker.PreviewChangeName(w.change.ID)
	fake.setDown(true)
	w.chats.SaveAfterTurn(s.ID) // saves; its rehearsal finds the worker down
	saved := w.branchTip()
	// The save's own rehearsal, however it runs, has asked and been refused.
	eventually(t, "the save's rehearsal asked the worker", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.refused > 0
	})
	w.chats.RehearseSaved(ctx, s, saved)
	if len(fake.bootsNamed(name)) != 0 || rehearsed(s) != "" || toldTheChat(s, "[apptown check]") {
		t.Fatal("a worker that could not be asked was taken as the app's failure, or marked the commit")
	}

	fake.setDown(false)
	fake.mu.Lock()
	fake.answer = func(spec worker.PreviewSpec) worker.PreviewState {
		return worker.PreviewState{Phase: docker.PhaseProbe, Error: "the app did not answer", LogTail: "crashed on start"}
	}
	fake.mu.Unlock()
	w.chats.RehearseSaved(ctx, s, saved)
	if len(fake.bootsNamed(name)) != 1 || rehearsed(s) != saved {
		t.Fatalf("a failing boot: boots %d, rehearsed %q", len(fake.bootsNamed(name)), rehearsed(s))
	}
	eventually(t, "the chat told of the failed boot", func() bool { return toldTheChat(s, "[apptown check]") && toldTheChat(s, saved[:12]) })

	w.chats.RehearseSaved(ctx, s, saved)
	if len(fake.bootsNamed(name)) != 1 {
		t.Fatal("a commit already rehearsed was booted again")
	}
}

// A second successful save during a boot must be drained after that boot;
// it cannot rely on a third turn ever arriving.
func TestASaveDuringARehearsalIsRehearsedNext(t *testing.T) {
	w, f, s := rehearsalWorld(t)
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.mu.Lock()
	f.answer = func(spec worker.PreviewSpec) worker.PreviewState {
		once.Do(func() { close(entered); <-resume })
		return worker.PreviewState{Running: true, ContainerID: "review", Incarnation: spec.Incarnation}
	}
	f.mu.Unlock()
	var release sync.Once
	defer release.Do(func() { close(resume) })
	w.chats.SaveAfterTurn(s.ID)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first rehearsal never booted")
	}
	newer := w.commitOnWorker(t, s.ID, "later.txt", "saved during boot\n")
	w.chats.SaveAfterTurn(s.ID)
	if tip := w.branchTip(); tip != newer {
		t.Fatalf("second save did not settle: %s", tip)
	}
	release.Do(func() { close(resume) })
	eventually(t, "second saved commit rehearsed without a third save", func() bool { return rehearsed(s) == newer })
}

// The same saved commit with different public inputs is a new incarnation.
func TestARehearsalFollowsTheIdentityNotOnlyTheCommit(t *testing.T) {
	w, f, s := rehearsalWorld(t)
	// Saved without queueing a rehearsal of its own: this test rehearses.
	saved := saveWithoutRehearsal(t, w, s)
	if saved == "" {
		t.Fatal("no saved commit")
	}
	w.chats.RehearseSaved(context.Background(), s, saved)
	name := docker.PreviewChangeName(w.change.ID)
	if len(f.bootsNamed(name)) != 1 {
		t.Fatal("first rehearsal did not boot")
	}
	if err := integration.NewAttachments(w.db).Remember(t.Context(), w.change.ProjectID, "errors", "glitchtip", `{"dsn":"https://new@errors.example/9"}`); err != nil {
		t.Fatal(err)
	}
	w.chats.RehearseSaved(context.Background(), s, saved)
	if len(f.bootsNamed(name)) != 2 {
		t.Fatal("new public inputs at the same saved commit were never rehearsed")
	}
}
