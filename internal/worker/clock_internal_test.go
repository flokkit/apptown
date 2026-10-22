package worker

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// The worker judges expiries, deadlines and leases by its own clock
// (Server.Now). Each test below sets that clock hours away from the real
// one: the worker's answer follows its clock, not the machine's.

// skewed is a clock d away from the real one.
func skewed(d time.Duration) func() time.Time {
	return func() time.Time { return time.Now().Add(d) }
}

// fakeDocker is an in-process Docker that prints out for the commands
// named in lines (by first argument) and nothing for the rest, and
// succeeds.
func fakeDocker(lines map[string]string) dockerFunc {
	return func(args []string) ([]byte, error) {
		if out, ok := lines[args[0]]; ok {
			return []byte(out + "\n"), nil
		}
		return nil, nil
	}
}

// stopRecorder is a backend that records which tasks it was told to stop,
// and says each it is asked about was created. Nothing else is used.
type stopRecorder struct {
	execution.TaskDriver
	mu      sync.Mutex
	stopped []string
}

func (d *stopRecorder) Stop(_ context.Context, ref execution.TaskRef) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = append(d.stopped, ref.Key)
	return nil
}

func (d *stopRecorder) Observe(context.Context, execution.TaskRef) (execution.TaskObservation, error) {
	return execution.TaskObservation{State: execution.TaskCreated}, nil
}

// A task's private Docker expires when the worker's clock passes its
// admission's expiry: the sweep stops it then, and not before.
func TestThePrivateSweepJudgesExpiryByTheWorkersClock(t *testing.T) {
	t.Parallel()
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	docker := fakeDocker(map[string]string{"ps": "agent-1 " + expires, "network": "agent-1 " + expires})
	for _, c := range []struct {
		name    string
		ahead   time.Duration
		stopped bool
	}{
		{"before its expiry", 0, false},
		{"past its expiry", 2 * time.Hour, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tasks := &stopRecorder{}
			s := &Server{State: t.TempDir(), Docker: docker, Tasks: tasks, Now: skewed(c.ahead)}
			if err := s.writeRecord(record{Key: "agent-1", Private: &privateSet{Network: "apptown-private-agent-1"}}); err != nil {
				t.Fatal(err)
			}
			s.SweepPrivate(context.Background())
			if got := len(tasks.stopped) == 1; got != c.stopped {
				t.Fatalf("with the worker's clock %s ahead, stopped %v", c.ahead, tasks.stopped)
			}
		})
	}
}

// A private Docker is not made for a task whose admission has expired on
// the worker's clock.
func TestAnExpiredAdmissionIsRefusedByTheWorkersClock(t *testing.T) {
	t.Parallel()
	s := &Server{State: t.TempDir(), Docker: fakeDocker(nil), Tasks: &stopRecorder{}, Now: skewed(2 * time.Hour)}
	rec := record{Key: "agent-1", Private: &privateSet{Network: "apptown-private-agent-1", Expires: time.Now().Add(time.Hour)}}
	if err := s.startPrivate(context.Background(), rec); !errors.Is(err, execution.ErrExpired) {
		t.Fatalf("an admission an hour past on the worker's clock: %v", err)
	}
}

// A task is ensured only while its admission holds on the worker's clock.
func TestEnsuringJudgesTheAdmissionByTheWorkersClock(t *testing.T) {
	t.Parallel()
	body := `{"key":"agent-1","image":"busybox","argv":["true"],"admission":{"profile":"worker","network":"none","expires":"` +
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}}`
	s := &Server{State: t.TempDir(), Tasks: &stopRecorder{}, Now: skewed(2 * time.Hour)}
	req := httptest.NewRequest("PUT", "/v1/tasks/agent-1", strings.NewReader(body))
	req.SetPathValue("key", "agent-1")
	w := httptest.NewRecorder()
	s.ensure(w, req)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("an admission an hour past on the worker's clock: %d %s", w.Code, w.Body.String())
	}
}

// A check's folder is past its deadline when the worker's clock is.
func TestTheCheckSweepJudgesDeadlinesByTheWorkersClock(t *testing.T) {
	t.Parallel()
	s := &Server{State: t.TempDir(), Now: skewed(2 * time.Hour)}
	folder := filepath.Join(s.checksRoot(), strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+"-c1")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	s.SweepChecks(context.Background(), fakeDocker(nil), false)
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("a check an hour past its deadline on the worker's clock kept its folder: %v", err)
	}
}

// A private service is given a minute, on the worker's clock, to become
// healthy.
func TestAServicesMinuteIsTheWorkersClocks(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	at := time.Now()
	// Each reading of the worker's clock is two minutes after the last.
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); at = at.Add(2 * time.Minute); return at }
	s := &Server{State: t.TempDir(), Docker: fakeDocker(map[string]string{"inspect": "running starting"}), Now: now}
	// A hang guard only: the minute is the worker's clock's.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.healthy(ctx, "apptown-private-agent-1-svc"); err == nil || !strings.Contains(err.Error(), "within a minute") {
		t.Fatalf("a service starting past its minute on the worker's clock: %v", err)
	}
}

// An AppTown's session number is above its clock, and above the last one.
func TestASessionNumberIsAboveAppTownsClock(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "worker-session")
	ahead := time.Now().Add(24 * time.Hour)
	next, err := NextSession(file, func() time.Time { return ahead })
	if err != nil || next != ahead.UnixNano() {
		t.Fatalf("a session with the clock a day ahead: %d %v, want %d", next, err, ahead.UnixNano())
	}
	again, err := NextSession(file, func() time.Time { return time.Unix(0, 0) })
	if err != nil || again != next+1 {
		t.Fatalf("a session with the clock behind the last: %d %v, want %d", again, err, next+1)
	}
}
