package workflow

import "testing"

// A check receipt recorded before the AppTown rename keeps its platform
// fault (written forgeFault then); receipts are written as apptownFault
// (review of the AppTown rename, R2).
//
// Kills: CheckReceipt reading only the current key.
func TestAPreRenameCheckKeepsItsPlatformFault(t *testing.T) {
	t.Parallel()
	facts, err := ReadCheckEvidence([]byte(`{"checks":[{"check":"readiness","verdict":"cannot-assess","forgeFault":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Checks) != 1 || !facts.Checks[0].ForgeFault {
		t.Fatal("historical platform failure silently became a check without a platform fault")
	}
}
