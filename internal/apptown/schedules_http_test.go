package apptown

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A project's schedules are a project read: its members see them, someone
// outside the project is refused, and a failed read of the firing records
// fails rather than answering that the project has no schedules.
func TestProjectSchedulesAreAProjectReadThatNeverReadsFailureAsNone(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	revokeReadingForAll(t, a) // only the project's own people read it
	h := a.Handler()
	yaml := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: monthly}
spec:
  kind: pipeline
  on:
    schedule: {cron: "0 6 1 * *"}
  stages:
    - name: review
      steps:
        - name: approve
          uses: approval
          with: {approvers: [owner], means: "the monthly review is complete"}
`
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), yaml, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Adoption.SelectLane(t.Context(), idstest.N(1), "monthly", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Adoption.RecordCurrent(t.Context(), idstest.N(1), adoption.AdoptionEdit, "fixture selects monthly", "fixture"); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "outsider", "GET", "/api/v1/projects/game/schedules", ""); w.Code != 403 {
		t.Fatalf("outsider: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "owner", "GET", "/api/v1/projects/game/schedules", "")
	var got httpapi.ScheduleList
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("owner: %d %s", w.Code, w.Body.String())
	}
	if len(got.Schedules) != 1 || got.EvalCopy {
		t.Fatalf("schedules: %s", w.Body.String())
	}
	if s := got.Schedules[0]; s.Source.Workflow != "monthly" || s.Cron != "0 6 1 * *" || s.TimeZone != "UTC" || s.Next == "" || s.Last != nil {
		t.Fatalf("schedule: %+v", s)
	}
	dbtest.FailNextStatement(t, a.DB, "name: WorkflowSchedulesOfProject :many")
	if w := authzCall(h, "owner", "GET", "/api/v1/projects/game/schedules", ""); w.Code < 500 {
		t.Fatalf("a failed read answered %d: %s", w.Code, w.Body.String())
	}
}
