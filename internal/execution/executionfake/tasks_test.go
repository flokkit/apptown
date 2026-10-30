package executionfake

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executiontest"
)

func fakeSpec(key, output string, exit int, block bool) execution.TaskSpec {
	env := []string{"FAKE_OUTPUT=" + output, fmt.Sprintf("FAKE_EXIT=%d", exit)}
	if block {
		env = append(env, "FAKE_BLOCK=1")
	}
	return execution.TaskSpec{Key: key, Image: "fake", Argv: []string{"run"}, Env: env,
		Admission: execution.Admission{Profile: "fake", Network: execution.NetworkNone}}
}

// The fake keeps the contract its callers' tests rely on.
func TestTheFakeTaskDriverKeepsTheContract(t *testing.T) {
	executiontest.Run(t, executiontest.Harness{Driver: &Tasks{}, Spec: fakeSpec, Key: executiontest.UniqueKeys("fake")})
}

// An admission expires on the clock the fake is given, not the system's: a
// caller whose clock is behind the real one (a test that moved AppTown's
// clock back) creates tasks admitted on it.
func TestAnAdmissionExpiresOnTheGivenClock(t *testing.T) {
	then := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	f := &Tasks{Now: func() time.Time { return then }}
	spec := fakeSpec("in-time", "", 0, false)
	spec.Admission.Expires = then.Add(time.Minute)
	if _, err := f.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("a task admitted until a minute after the clock's now: %v", err)
	}
	spec = fakeSpec("expired", "", 0, false)
	spec.Admission.Expires = then
	if _, err := f.Ensure(context.Background(), spec); !errors.Is(err, execution.ErrExpired) {
		t.Fatalf("a task whose admission expired at the clock's now: %v, want ErrExpired", err)
	}
}
