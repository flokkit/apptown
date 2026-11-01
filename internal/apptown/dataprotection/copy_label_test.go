package dataprotection

import "testing"

func TestCopyLabelsKeepOldOrMissingCopiesSeparateFromUnreachableHosts(t *testing.T) {
	for _, tc := range []struct {
		state CopyState
		label CopyLabel
	}{{CopyFresh, copyLabelFresh}, {CopyStale, copyLabelOverdue}, {copyUnknown, copyLabelUnknown}, {CopyNone, copyLabelMissing}, {CopyDisposable, copyLabelDisposable}} {
		if got := tc.state.label(); got != tc.label {
			t.Fatalf("%s: %s", tc.state, got)
		}
	}
}
