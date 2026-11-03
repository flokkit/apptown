package workflowexec

import (
	"reflect"
	"testing"
)

func TestReportBlockersRequiresAPersonAction(t *testing.T) {
	t.Parallel()
	for _, report := range []string{
		"Blockers:\n- None", "Blockers:\n- None.", "Blockers:\n- none",
		"Blockers:\n- N/A", "Blockers:\n- No blockers", "Blockers:\n- NO BLOCKERS!",
		"Blockers:\n- n/a...", "Blockers:\n- None.\n- N/A.\n- No blockers.",
		"Blockers: none", "Blockers: NONE.",
	} {
		t.Run(report, func(t *testing.T) {
			if got := reportBlockers(report); len(got) != 0 {
				t.Fatalf("person actions = %q", got)
			}
		})
	}
	for _, report := range []string{
		"Blockers:\n- Choose the retention period.",
		"Blockers:\n- None.\n- Choose the retention period.",
	} {
		if got := reportBlockers(report); !reflect.DeepEqual(got, []string{"Choose the retention period."}) {
			t.Fatalf("lost person action: %q", got)
		}
	}
}
