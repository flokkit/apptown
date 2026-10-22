package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/complaint"
)

func TestComplaintSteeringIsFixedText(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	NewComplaints(nil, nil, complaint.AgentInstructions, complaint.ConsoleRules).ComplaintSteering(w, httptest.NewRequest("GET", "/api/v1/complaints/steering", nil))
	if w.Code != 200 {
		t.Fatalf("steering: %d", w.Code)
	}
	var out struct {
		Step    string `json:"step"`
		Console string `json:"console"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Step, "complaints box") || !strings.Contains(out.Console, "GUARANTEE") {
		t.Fatalf("steering text missing: %+v", out)
	}
}
