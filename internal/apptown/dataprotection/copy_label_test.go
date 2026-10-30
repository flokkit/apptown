package dataprotection

import "testing"

func TestCopyLabelsKeepOldOrMissingCopiesSeparateFromUnreachableHosts(t *testing.T) {
	for _, tc := range []struct {
		state CopyState
		label CopyLabel
	}{{CopyFresh, CopyLabelFresh}, {CopyStale, CopyLabelOverdue}, {CopyUnknown, CopyLabelUnknown}, {CopyNone, CopyLabelMissing}, {CopyDisposable, CopyLabelDisposable}} {
		if got := tc.state.Label(); got != tc.label {
			t.Fatalf("%s: %s", tc.state, got)
		}
	}
}
