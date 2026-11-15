package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJanitorProjectKeepsSchedulerProblemOnWire(t *testing.T) {
	var row JanitorProject
	if err := json.Unmarshal([]byte(`{"state":{"word":"up to date"}}`), &row); err != nil {
		t.Fatal(err)
	}
	row.Problem = "internal workflow problem"
	row.ScheduleProblem = "queue security was not read"
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"scheduleProblem":"queue security was not read"`) || strings.Contains(string(body), `"problem":`) {
		t.Fatal(string(body))
	}
}
