package main

import (
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
// may run beside a serving forge, does not.
func TestEveryStartButAPasswordResetHoldsTheDatabase(t *testing.T) {
	t.Parallel()
	for name, set := range map[string]func(*daemonFlags){
		"serving":                 func(*daemonFlags) {},
		"-verify-only":            func(o *daemonFlags) { o.verifyOnly = true },
		"-drain-local-docker":     func(o *daemonFlags) { o.drainLocalDocker = true },
		"-backfill-recorded-main": func(o *daemonFlags) { o.backfillMain = true },
	} {
		o := &daemonFlags{}
		set(o)
		if !o.holdsDatabase() {
			t.Errorf("%s starts without the one-daemon lock", name)
		}
	}
	if (&daemonFlags{resetPassword: "alice"}).holdsDatabase() {
		t.Error("-reset-password must run beside a serving forge")
	}
}
