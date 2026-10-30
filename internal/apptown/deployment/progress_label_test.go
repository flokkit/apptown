package deployment

import "testing"

func TestProgressLabelsKeepReadinessRestoreAndNeedsSeparateFromRuntime(t *testing.T) {
	t.Parallel()
	for state, want := range map[Readiness]ProgressLabel{0: ProgressLabelUnknown, Reconciled: ProgressLabelDeployed, Reconciling: ProgressLabelDeploying, ReadinessUncertain: ProgressLabelUnknown, ReadinessFailed: ProgressLabelFailed} {
		if got := state.Display(); got != want {
			t.Errorf("readiness %v: %v, want %v", state, got, want)
		}
	}
	for state, want := range map[RestoreCheck]ProgressLabel{"": ProgressLabelUnknown, RestoreUnconfirmed: ProgressLabelWaiting, RestoreGone: ProgressLabelFailed, RestoreFilling: ProgressLabelWorking, RestoreNotFilled: ProgressLabelFailed} {
		if got := state.Display(); got != want {
			t.Errorf("restore %v: %v, want %v", state, got, want)
		}
	}
	for state, want := range map[NeedState]ProgressLabel{0: ProgressLabelUnknown, NeedReady: ProgressLabelPassed, NeedCreating: ProgressLabelWorking, NeedFailed: ProgressLabelFailed, NeedOrphaned: ProgressLabelStopped} {
		if got := state.Display(); got != want {
			t.Errorf("need %v: %v, want %v", state, got, want)
		}
	}
}

func TestOverrideLabelDistinguishesRequestedAndApplied(t *testing.T) {
	t.Parallel()
	if OverrideDisplay(true) != ProgressLabelExcepted || OverrideDisplay(false) != ProgressLabelWaiting {
		t.Fatal("override application label")
	}
}
