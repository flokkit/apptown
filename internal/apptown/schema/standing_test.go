package schema

import (
	"strings"
	"testing"
)

func TestStandingDeclarationsAndUTCWords(t *testing.T) {
	for expr, want := range map[string]string{"0 5 * * *": "every day at 05:00 UTC", "0 6 * * 1": "every Monday at 06:00 UTC", "0 6 1 * *": "on day 1 of each month at 06:00 UTC", "*/5 * * * *": "on cron `*/5 * * * *` (UTC)"} {
		if got := CronWords(expr); got != want {
			t.Errorf("%s: %s", expr, got)
		}
	}
	base := "kind: Workflow\nmetadata: {name: maintenance, title: Care}\nspec:\n  kind: pipeline\n  on:\n    schedule: {cron: '0 6 * * 1', aggressiveCron: '0 6 * * *', queue: deps, standing: true}\n  stages:\n    - name: work\n      steps:\n        - name: repair\n          uses: agent/pi\n          with: {repairRounds: 3}\n"
	doc, err := ParseWorkflow([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	st, ok := doc.Standing()
	if !ok || st.Queue != "deps" || st.CronFor("aggressive") != "0 6 * * *" || st.CronFor("routine") != "0 6 * * 1" || doc.RepairRounds() != 3 || doc.Metadata.DisplayTitle() != "Care" {
		t.Fatalf("%+v %+v", doc, st)
	}
	for _, invalid := range []string{strings.Replace(base, "standing: true", "standing: false", 1), strings.Replace(base, "aggressiveCron: '0 6 * * *'", "aggressiveCron: invalid", 1), strings.Replace(base, "schedule: {cron: '0 6 * * 1', aggressiveCron: '0 6 * * *', queue: deps, standing: true}", "publication: {queue: deps, standing: false}", 1)} {
		if _, err := ParseWorkflow([]byte(invalid)); err == nil {
			t.Fatal("accepted invalid standing declaration")
		}
	}
	pub := strings.Replace(base, "schedule: {cron: '0 6 * * 1', aggressiveCron: '0 6 * * *', queue: deps, standing: true}", "publication: {queue: moves, standing: true}", 1)
	doc, err = ParseWorkflow([]byte(pub))
	if err != nil {
		t.Fatal(err)
	}
	st, ok = doc.Standing()
	if !ok || !st.Publication || st.Queue != "moves" {
		t.Fatal(st)
	}
}
