package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

func TestSlotConsumersNamesWorkflows(t *testing.T) {
	t.Parallel()
	ways := wow.New(databaseSuite.New(t), wow.Dependencies{})
	// A library workflow whose YAML names the 'plan' slot on a step.
	_, _, err := ways.PublishWorkflow(t.Context(), `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: planner}
spec:
  kind: lifecycle
  stages:
    - name: think
      steps:
        - {name: draft, uses: agent/pi, with: {model: plan}}
`, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	c, err := (ModelSlots{ways: ways}).slotConsumers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Built-in consumers are always present.
	if len(c["main-strong"]) == 0 || len(c["chat"]) == 0 {
		t.Fatalf("built-in consumers missing: %+v", c)
	}
	for slot, want := range map[string][]string{
		"main-strong": {"change summaries", "failure explanations", "approval briefs", "tasks left unset"},
		"fast":        {"project descriptions"},
		"checks":      {"ways-of-working model checks", "fleet conformance checks"},
		"scout":       {"helpers' quick lookups"},
	} {
		for _, job := range want {
			if !slices.Contains(c[slot], job) {
				t.Errorf("%s missing consumer %q: %v", slot, job, c[slot])
			}
		}
	}
	if slices.Contains(c["fast"], "change summaries") {
		t.Error("change summaries use main-strong, not fast")
	}
	// The 'plan' slot now has a workflow consumer naming the sample.
	found := false
	for _, s := range c["plan"] {
		if s == "workflow library/planner" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan slot must list the workflow that names it: %+v", c["plan"])
	}
}

// Retired choices cannot create unused cells or extra matrix rows.
func TestPutModelSlotRejectsRetiredChoices(t *testing.T) {
	t.Parallel()
	db, accounts := accountHTTPFixture(t)
	models := model.New(db)
	h := ModelSlots{models: models, access: accounts}
	for _, slot := range []string{"default", "primary-fast"} {
		t.Run(slot, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/v1/model-slots", strings.NewReader(fmt.Sprintf(`{"slot":%q,"pattern":"vendor/model"}`, slot)))
			req = req.WithContext(access.WithCaller(req.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Via: "session"}, Verified: true, Receipt: "boss"}))
			w := httptest.NewRecorder()
			h.PutModelSlot(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("retired choice: %d %s", w.Code, w.Body)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("unknown model choice %q: edit the model request to use main-strong or main-fast; the other basic choices are secondary-strong and secondary-fast", slot)
			if body.Error != want {
				t.Fatalf("error = %q, want %q", body.Error, want)
			}
		})
	}
	cells, err := models.Cells(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 0 {
		t.Fatalf("refused cells were saved: %v", cells)
	}
	if receipts := receiptsOfKind(t, db, "model-slot.set"); len(receipts) != 0 {
		t.Fatalf("refused changes recorded: %+v", receipts)
	}
}
