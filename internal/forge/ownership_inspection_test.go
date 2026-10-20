package forge

import (
	"net/http"
	"strings"
	"testing"
)

// The one-time ownership inspection is the platform administrator's: it
// reads what targets hold, and an approval records what AppTown may
// later delete. Another person is refused both; an approval of something
// the inspection does not propose now records nothing.
func TestTheOwnershipInspectionIsTheAdministrators(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	if w := authzCall(h, "outsider", http.MethodGet, "/api/v1/ownership/inspection", ""); w.Code != 403 {
		t.Fatalf("outsider inspection: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "outsider", http.MethodPost, "/api/v1/ownership/approvals", `{"keys":["deployment:1:volume:v"]}`); w.Code != 403 {
		t.Fatalf("outsider approval: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", http.MethodGet, "/api/v1/ownership/inspection", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"proposals":`) {
		t.Fatalf("inspection: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", http.MethodPost, "/api/v1/ownership/approvals", `{"keys":["deployment:1:volume:made-up"]}`); w.Code != 409 {
		t.Fatalf("an unproposed approval: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", http.MethodPost, "/api/v1/ownership/approvals", `{"keys":[]}`); w.Code != 400 {
		t.Fatalf("an empty approval: %d %s", w.Code, w.Body.String())
	}
}
