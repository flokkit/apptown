package workflowexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker/workertest"
)

// recordingDriver keeps every request sent to Ensure, as bytes, and the
// identity each answer named. loseEnsure and loseStart make the first reply
// of each lost after the driver acted; failRelease refuses releases and
// blindObserve makes the backend unobservable while set.
type recordingDriver struct {
	execution.TaskDriver
	loseEnsure, loseStart bool

	mu           sync.Mutex
	sent         [][]byte
	ids          []string
	releases     int
	failRelease  bool
	blindObserve bool
}

func (d *recordingDriver) Observe(ctx context.Context, ref execution.TaskRef) (execution.TaskObservation, error) {
	d.mu.Lock()
	blind := d.blindObserve
	d.mu.Unlock()
	if blind {
		return execution.TaskObservation{}, execution.ErrTaskUnobservable
	}
	return d.TaskDriver.Observe(ctx, ref)
}

func (d *recordingDriver) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	body, _ := json.Marshal(spec)
	obs, err := d.TaskDriver.Ensure(ctx, spec)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent, d.ids = append(d.sent, body), append(d.ids, obs.ID)
	if err == nil && d.loseEnsure {
		d.loseEnsure = false
		return execution.TaskObservation{}, execution.ErrWorkerUnavailable
	}
	return obs, err
}

func (d *recordingDriver) Start(ctx context.Context, ref execution.TaskRef) error {
	err := d.TaskDriver.Start(ctx, ref)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil && d.loseStart {
		d.loseStart = false
		return errors.New("the start reply was lost after the backend started it")
	}
	return err
}

func (d *recordingDriver) Release(ctx context.Context, ref execution.TaskRef) error {
	d.mu.Lock()
	d.releases++
	fail := d.failRelease
	d.mu.Unlock()
	if fail {
		return execution.ErrWorkerUnavailable
	}
	return d.TaskDriver.Release(ctx, ref)
}

func (d *recordingDriver) record() (sent [][]byte, ids []string, releases int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte{}, d.sent...), append([]string{}, d.ids...), d.releases
}

// keptRequest is the task request the forge kept for an operation, as the
// bytes it keeps, or nil when none is kept.
func keptRequest(t *testing.T, a *taskForge, operation string) []byte {
	t.Helper()
	path := filepath.Join(a.dir, "tasks", operation, "request.json")
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) { // released between the read and here
		return nil
	}
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the kept request's mode: %v %v", info, err)
	}
	var kept struct{ Task json.RawMessage }
	if err := json.Unmarshal(body, &kept); err != nil {
		t.Fatal(err)
	}
	return kept.Task
}

// A restarted forge sends the request the task was admitted with, byte for
// byte, not one rebuilt from its settings now: here the default image
// changed while it was down. The task runs once, on the admitted image, and
// its kept request is removed once it is released.
func TestARecoveredTaskIsSentTheRequestItWasAdmittedWith(t *testing.T) {
	t.Parallel()
	for _, backend := range taskBackends {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			a, fake, step := scriptTaskFixture(t, map[string]any{"run": "true", "executor": "docker", "env": map[string]any{"FAKE_EXIT": "0"}})
			if backend == "worker" {
				a.onWorker(t, fake)
			}
			driver := &recordingDriver{TaskDriver: a.tasks.Driver}
			a.tasks.Driver, a.tasks.AbandonAt = driver, func(s string) bool { return s == "created" }
			if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			key := taskKeyOf(step)
			admitted := keptRequest(t, a, key)
			if admitted == nil {
				t.Fatal("no request was kept for the task")
			}
			b := startTaskForge(t, a.db, a.dir, "another-agent-image", a.definitions)
			b.tasks.Driver, b.tasks.Remote, b.tasks.Profile, b.tasks.Backend, b.tasks.Poll = driver, a.tasks.Remote, a.tasks.Profile, a.tasks.Backend, a.tasks.Poll
			b.recover(t)
			awaitStep(t, b, step.ID, workflow.StatusPass)
			sent, ids, _ := driver.record()
			if len(sent) != 2 || !bytes.Equal(sent[0], admitted) || !bytes.Equal(sent[1], admitted) {
				t.Fatalf("sent %d requests; the recovered one is the admitted one: %v", len(sent), len(sent) == 2 && bytes.Equal(sent[1], admitted))
			}
			var spec execution.TaskSpec
			if err := json.Unmarshal(sent[1], &spec); err != nil || spec.Image != "forge-agent" {
				t.Fatalf("the recovered request's image: %q %v", spec.Image, err)
			}
			if ids[0] != ids[1] || fake.Starts(key) != 1 {
				t.Fatalf("tasks %v, started %d times", ids, fake.Starts(key))
			}
			if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return keptRequest(t, b, key) == nil }) {
				t.Fatal("the released task's request is still kept")
			}
		})
	}
}

// A lost ensure reply and a lost start reply are asked again with the same
// request: the backend's ensure finds the task it made, and it runs once.
func TestLostEnsureAndStartRepliesMakeOneTask(t *testing.T) {
	t.Parallel()
	for _, backend := range taskBackends {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			a, fake, step := fakeTaskFixture(t, false)
			if backend == "worker" {
				a.onWorker(t, fake)
			}
			driver := &recordingDriver{TaskDriver: a.tasks.Driver, loseEnsure: true, loseStart: true}
			a.tasks.Driver = driver
			if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			awaitStep(t, a, step.ID, workflow.StatusPass)
			sent, ids, _ := driver.record()
			if len(sent) != 2 || !bytes.Equal(sent[0], sent[1]) || ids[0] == "" || ids[0] != ids[1] {
				t.Fatalf("ensured %d times for tasks %v; the same request: %v", len(sent), ids, len(sent) == 2 && bytes.Equal(sent[0], sent[1]))
			}
			if starts := fake.Starts(taskKeyOf(step)); starts != 1 {
				t.Fatalf("the task started %d times", starts)
			}
		})
	}
}

// A release the backend does not confirm leaves the settled attempt
// unreleased, with its request kept; the forge's release pass tries it again
// until the backend confirms, records that, and then never again.
func TestAFailedReleaseIsTriedAgainUntilConfirmed(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	driver := &recordingDriver{TaskDriver: fake, failRelease: true}
	a.tasks.Driver = driver
	ctx := context.Background()
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusPass)
	key := taskKeyOf(step)
	records := tasks.New(a.db)
	unreleased := func() tasks.Attempt {
		t.Helper()
		got, err := records.Get(ctx, key)
		if err != nil || got.Observed != tasks.Settled {
			t.Fatalf("the attempt: %+v %v", got, err)
		}
		return got
	}
	if got := unreleased(); got.Released || !fake.Present(key) || keptRequest(t, a, key) == nil {
		t.Fatalf("after a refused release: released=%v present=%v", got.Released, fake.Present(key))
	}
	if n, err := a.tasks.ReleaseSettled(ctx); err != nil || n != 0 || unreleased().Released {
		t.Fatalf("a pass while the backend refuses: %d %v", n, err)
	}
	// A backend that cannot say what holds the key is not asked to remove
	// anything, and the attempt stays for the next pass.
	driver.mu.Lock()
	driver.failRelease, driver.blindObserve = false, true
	driver.mu.Unlock()
	if n, err := a.tasks.ReleaseSettled(ctx); err != nil || n != 0 || unreleased().Released || !fake.Present(key) {
		t.Fatalf("a pass while the backend cannot be observed: %d %v", n, err)
	}
	driver.mu.Lock()
	driver.blindObserve = false
	driver.mu.Unlock()
	if n, err := a.tasks.ReleaseSettled(ctx); err != nil || n != 1 {
		t.Fatalf("a pass once the backend answers: %d %v", n, err)
	}
	if got := unreleased(); !got.Released || fake.Present(key) || keptRequest(t, a, key) != nil {
		t.Fatalf("after a confirmed release: released=%v present=%v", got.Released, fake.Present(key))
	}
	_, _, before := driver.record()
	if n, err := a.tasks.ReleaseSettled(ctx); err != nil || n != 0 {
		t.Fatalf("a pass after the release: %d %v", n, err)
	}
	if _, _, after := driver.record(); after != before || before != 3 {
		t.Fatalf("releases asked: %d, then %d", before, after)
	}
}

// A task recorded before requests were kept has none: recovery rebuilds its
// request and, when the forge's settings changed since, refuses it instead
// of running something other than what was admitted.
func TestATaskWithoutAKeptRequestIsRefusedWhenItsRequestChanged(t *testing.T) {
	t.Parallel()
	a, fake, step := scriptTaskFixture(t, map[string]any{"run": "true", "executor": "docker", "env": map[string]any{"FAKE_EXIT": "0"}})
	a.tasks.AbandonAt = func(s string) bool { return s == "created" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	// Made as an earlier forge left it: the record without a kept request.
	if err := os.RemoveAll(filepath.Join(a.dir, "tasks", key)); err != nil {
		t.Fatal(err)
	}
	b := startTaskForge(t, a.db, a.dir, "another-agent-image", a.definitions)
	b.tasks.Driver, b.tasks.Poll = fake, a.tasks.Poll
	b.recover(t)
	settled := awaitStep(t, b, step.ID, workflow.StatusError)
	if !bytes.Contains(settled.Evidence, []byte("request changed")) || fake.Starts(key) != 0 {
		t.Fatalf("started %d times; evidence %s", fake.Starts(key), settled.Evidence)
	}
}

// A settled attempt whose release was never confirmed and whose provider ID
// was never recorded (a row from before releases were recorded, migration
// 97) is released once the task under its key is observed to carry its
// request.
func TestAReleasePassRemovesAnOwnTaskFoundByItsRequest(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	driver := &recordingDriver{TaskDriver: fake, failRelease: true}
	a.tasks.Driver = driver
	ctx := context.Background()
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusPass)
	key := taskKeyOf(step)
	// No owner operation clears a recorded provider ID; this represents an
	// old row that has none, so ownership rests on the request digest alone.
	// sql-guarantee: an unreleased settled row without a provider ID; no operation clears one.
	if _, err := a.db.Exec(`UPDATE task_attempts SET provider_id='' WHERE operation=$1`, key); err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	driver.failRelease = false
	driver.mu.Unlock()
	if n, err := a.tasks.ReleaseSettled(ctx); err != nil || n != 1 {
		t.Fatalf("the release pass: %d %v", n, err)
	}
	got, err := tasks.New(a.db).Get(ctx, key)
	if err != nil || !got.Released || fake.Present(key) {
		t.Fatalf("after the pass: %+v %v present=%v", got, err, fake.Present(key))
	}
}

// Migration 97 left every old settled row unreleased, including attempts
// refused because another request's task held their key. The release pass
// leaves that task alone and records the attempt released: nothing of its
// own is held.
func TestAReleasePassLeavesAnotherRequestsTaskUnderTheKey(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	ctx := context.Background()
	key := taskKeyOf(step)
	foreign, err := fake.Ensure(ctx, execution.TaskSpec{Key: key, Image: "foreign", Argv: []string{"true"}, Env: []string{"FAKE_BLOCK=1"}, Admission: execution.Admission{Profile: "foreign", Network: execution.NetworkNone}})
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: key, ProviderID: foreign.ID}
	if err := fake.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fake.Release(ctx, ref) })
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	records := tasks.New(a.db)
	recorded, err := records.Get(ctx, key)
	if err != nil || recorded.Observed != tasks.Settled || !recorded.Released {
		t.Fatalf("refusal: %+v %v", recorded, err)
	}
	// No owner operation makes this historical state: migration 97 adds NULL
	// released_at to every old settled row, including key-conflict refusals.
	// sql-guarantee: a settled refusal left unreleased by migration 97; SettleNothingHeld always marks it released.
	if _, err := a.db.Exec(`UPDATE task_attempts SET released_at=NULL WHERE operation=$1`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := a.tasks.ReleaseSettled(ctx); err != nil {
		t.Fatal(err)
	}
	obs, err := fake.Observe(ctx, ref)
	if err != nil || obs.State != execution.TaskRunning {
		t.Fatalf("release retry removed a foreign task: %+v %v", obs, err)
	}
	if got, err := records.Get(ctx, key); err != nil || !got.Released {
		t.Fatalf("the refused attempt after the pass: %+v %v", got, err)
	}
}

// A worker's refusal of a request is acted on by its code, never its
// status: a request it cannot run, or another request's under the key, was
// refused with nothing made, and the attempt is settled as refused; a
// refusal whose code the forge does not know leaves the attempt's outcome
// unknown, never refused, on a 409 as on a 422.
func TestAWorkerRefusalIsSettledByItsCode(t *testing.T) {
	t.Parallel()
	// A refused attempt holds nothing, so it ends settled.
	for _, c := range []struct {
		status int
		code   string
		want   tasks.Observed
	}{
		{http.StatusUnprocessableEntity, "unsupported", tasks.Settled},
		{http.StatusConflict, "conflict", tasks.Settled},
		{http.StatusUnprocessableEntity, "later-class", tasks.Unknown},
		{http.StatusConflict, "later-class", tasks.Unknown},
	} {
		code, want := c.code, c.want
		t.Run(fmt.Sprintf("%s on %d", c.code, c.status), func(t *testing.T) {
			t.Parallel()
			a, fake, step := fakeTaskFixture(t, false)
			ca := workertest.NewCA(t)
			srv := workertest.StartWith(t, ca, t.TempDir(), fake, func(h http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && r.URL.Path == "/v1/tasks/"+taskKeyOf(step) {
						w.Header().Set("Content-Type", execution.RefusalContentType)
						w.WriteHeader(c.status)
						_, _ = w.Write([]byte(`{"code":"` + code + `","detail":"not today"}`))
						return
					}
					h.ServeHTTP(w, r)
				})
			})
			driver := workertest.Driver(t, ca, srv)
			a.tasks.Driver, a.tasks.Remote, a.tasks.Profile.Name, a.tasks.Backend = driver, driver, "worker", "worker at "+driver.URL
			if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			awaitStep(t, a, step.ID, workflow.StatusError)
			var attempt tasks.Attempt
			var err error
			poll.Until(context.Background(), 30*time.Second, 10*time.Millisecond, func() bool {
				attempt, err = tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
				return err != nil || attempt.Observed == want
			})
			if err != nil || attempt.Observed != want || !strings.Contains(attempt.Detail, "not today") {
				t.Fatalf("the attempt: %+v %v; want %s", attempt, err, want)
			}
		})
	}
}
