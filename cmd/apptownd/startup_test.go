package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRequiredRecoveryGatesWritableStartup(t *testing.T) {
	failure := errors.New("database repair failed")
	var started bool
	err := recoverThenStart(func() error { return failure }, func() error { started = true; return nil })
	if !errors.Is(err, failure) || started {
		t.Fatalf("failed repair allowed startup: started=%v error=%v", started, err)
	}
	var order []string
	err = recoverThenStart(func() error { order = append(order, "repair"); return nil }, func() error {
		order = append(order, "resume and serve")
		return nil
	})
	if err != nil || !reflect.DeepEqual(order, []string{"repair", "resume and serve"}) {
		t.Fatalf("startup order: %v error=%v", order, err)
	}
}

// Every start that may repair or settle work takes the one-daemon lock
// before migrating; only a password reset, which the operations guide says
// may run beside a serving AppTown, does not.
func TestEveryStartButAPasswordResetHoldsTheDatabase(t *testing.T) {
	t.Parallel()
	for name, set := range map[string]func(*daemonFlags){
		"serving":      func(*daemonFlags) {},
		"-verify-only": func(o *daemonFlags) { o.verifyOnly = true },
	} {
		o := &daemonFlags{}
		set(o)
		if !o.holdsDatabase() {
			t.Errorf("%s starts without the one-daemon lock", name)
		}
	}
	if (&daemonFlags{resetPassword: "alice"}).holdsDatabase() {
		t.Error("-reset-password must run beside a serving AppTown")
	}
}

// The shipped scanner bindings are synced before recovered work resumes: a
// scan resumed across an upgrade reads its binding when it starts, and must
// read the new one.
func TestStockScannersAreSyncedBeforeWorkResumes(t *testing.T) {
	t.Parallel()
	var order []string
	resumeWork(context.Background(),
		func(context.Context) { order = append(order, "sync stock scanners") },
		func(context.Context) { order = append(order, "resume pending steps") })
	if want := []string{"sync stock scanners", "resume pending steps"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("start-up order: %v, want %v", order, want)
	}
}

func TestNotificationAssemblyExcludesVerificationAndExplicitDisable(t *testing.T) {
	for _, tc := range []struct {
		name            string
		verify, enabled bool
		want            int
	}{
		{"normal", false, true, 1}, {"verification", true, true, 0}, {"disabled", false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			flags := daemonFlags{verifyOnly: tc.verify, updateChecks: tc.enabled}
			tasks := flags.notificationTasks(func(ctx context.Context) error { calls++; return ctx.Err() })
			if len(tasks) != tc.want {
				t.Fatalf("startup assembled %d notification loops, want %d", len(tasks), tc.want)
			}
			for _, task := range tasks {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if err := task.run(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("lifetime lost: %v", err)
				}
			}
			if calls != tc.want {
				t.Fatalf("notification effects %d want %d", calls, tc.want)
			}
		})
	}
}
