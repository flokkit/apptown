package deployment

import "testing"

func TestProgressLabelsKeepReadinessRestoreAndNeedsSeparateFromRuntime(t *testing.T) {
	t.Parallel()
	for state, want := range map[Readiness]ProgressLabel{0: progressLabelUnknown, Reconciled: progressLabelDeployed, reconciling: progressLabelDeploying, ReadinessUncertain: progressLabelUnknown, ReadinessFailed: progressLabelFailed} {
		if got := state.Display(); got != want {
			t.Errorf("readiness %v: %v, want %v", state, got, want)
		}
	}
	for state, want := range map[RestoreCheck]ProgressLabel{"": progressLabelUnknown, RestoreUnconfirmed: progressLabelWaiting, restoreGone: progressLabelFailed, RestoreFilling: progressLabelWorking, RestoreNotFilled: progressLabelFailed} {
		if got := state.Display(); got != want {
			t.Errorf("restore %v: %v, want %v", state, got, want)
		}
	}
	for state, want := range map[needState]ProgressLabel{0: progressLabelUnknown, NeedReady: progressLabelPassed, needCreating: progressLabelWorking, needFailed: progressLabelFailed, needOrphaned: progressLabelStopped} {
		if got := state.Display(); got != want {
			t.Errorf("need %v: %v, want %v", state, got, want)
		}
	}
}

func TestOverrideLabelDistinguishesRequestedAndApplied(t *testing.T) {
	t.Parallel()
	if overrideDisplay(true) != progressLabelExcepted || overrideDisplay(false) != progressLabelWaiting {
		t.Fatal("override application label")
	}
}
