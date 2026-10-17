package forge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/wow"
)

func TestGuidanceHTTPVersionsKeepProseChecksAndRejectedTagsTogether(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	base := "/api/v1/guidance/blocks"
	create := `{"name":"team/review","bodyMd":"Read carefully","tags":"team,review"}`
	if w := authzCall(h, "outsider", "POST", base, create); w.Code != 403 {
		t.Fatalf("foreign create: %d", w.Code)
	}
	w := authzCall(h, "boss", "POST", base, create)
	var block wow.Block
	if err := json.Unmarshal(w.Body.Bytes(), &block); err != nil || w.Code != 201 || block.ID.IsZero() {
		t.Fatalf("create: %d %s %v", w.Code, w.Body.String(), err)
	}
	path := fmt.Sprintf("%s/%d", base, block.ID)
	for _, tc := range []struct {
		body string
		code int
	}{{`{"name":""}`, 400}, {create, 409}} {
		if w := authzCall(h, "boss", "POST", base, tc.body); w.Code != tc.code {
			t.Fatalf("invalid or duplicate creation: %d %s", w.Code, w.Body.String())
		}
	}
	for _, path := range []string{base + "/bad", base + "/0000000f-423f-7000-8000-000000000000"} {
		want := 404
		if strings.HasSuffix(path, "/bad") {
			want = 400
		}
		if w := authzCall(h, "boss", "PATCH", path, `{"tags":"must-not-stick"}`); w.Code != want {
			t.Fatalf("invalid edit target: %d %s", w.Code, w.Body.String())
		}
	}

	check := func(query, body string, version, checks int) {
		t.Helper()
		w := authzCall(h, "owner", "GET", path+query, "")
		var out struct {
			Block        wow.Block
			ShownVersion int
			Checks       []json.RawMessage
			Versions     []json.RawMessage
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.ShownVersion != version || out.Block.BodyMD != body || len(out.Checks) != checks {
			t.Fatalf("version read: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	check("", "Read carefully", 1, 0)
	invalid := `{"tags":"must-not-stick","checks":[{"name":"bad","evaluator":"invented"}]}`
	if w := authzCall(h, "boss", "PATCH", path, invalid); w.Code != 400 {
		t.Fatalf("invalid checks: %d %s", w.Code, w.Body.String())
	}
	// A misspelt key in a check is refused, never saved as a check judged
	// without it (severty would have left the default severity).
	misspelt := `{"tags":"must-not-stick","checks":[{"name":"readme","expects":"README exists","severty":"high","sh":"test -f README.md"}]}`
	if w := authzCall(h, "boss", "PATCH", path, misspelt); w.Code != 400 || !strings.Contains(w.Body.String(), "severty") {
		t.Fatalf("misspelt check key: %d %s", w.Code, w.Body.String())
	}
	stored, err := a.Ways.BlockByID(t.Context(), block.ID)
	if err != nil || stored.Tags != "team,review" || stored.LatestVersion != 1 {
		t.Fatalf("rejected tags persisted: %+v %v", stored, err)
	}
	next := `{"bodyMd":"Read carefully, then approve","checks":[{"name":"readme","expects":"README exists","severity":"medium","evaluator":"sh","sh":"test -f README.md"}]}`
	if w := authzCall(h, "outsider", "PATCH", path, next); w.Code != 403 {
		t.Fatalf("foreign patch: %d", w.Code)
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: InsertBlockVersion")
	failed := `{"tags":"must-not-stick","bodyMd":"must-not-stick"}`
	if w := authzCall(h, "boss", "PATCH", path, failed); w.Code != 500 {
		t.Fatalf("failed atomic edit reported success: %d %s", w.Code, w.Body.String())
	}
	stored, err = a.Ways.BlockByID(t.Context(), block.ID)
	if err != nil || stored.Tags != "team,review" || stored.LatestVersion != 1 {
		t.Fatalf("failed HTTP edit persisted metadata: %+v %v", stored, err)
	}
	check("", "Read carefully", 1, 0)
	if w := authzCall(h, "boss", "PATCH", path, next); w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	check("", "Read carefully, then approve", 2, 1)
	check("?version=1", "Read carefully", 1, 0)
	if w := authzCall(h, "boss", "PATCH", path, next); w.Code != 200 {
		t.Fatalf("same patch: %d %s", w.Code, w.Body.String())
	}
	stored, err = a.Ways.BlockByID(t.Context(), block.ID)
	if err != nil || stored.LatestVersion != 2 {
		t.Fatalf("identical patch cut version: %+v %v", stored, err)
	}
	if w := authzCall(h, "boss", "PATCH", path, `{"tags":"other"}`); w.Code != 200 {
		t.Fatalf("tags: %d", w.Code)
	}
	for _, q := range []string{"?q=team/review", "?tag=other"} {
		w := authzCall(h, "owner", "GET", base+q, "")
		var blocks []wow.Block
		if err := json.Unmarshal(w.Body.Bytes(), &blocks); err != nil || w.Code != 200 || len(blocks) != 1 || blocks[0].ID != block.ID {
			t.Fatalf("filtered blocks: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	for _, tc := range []struct {
		path string
		code int
	}{{base + "/bad", 400}, {base + "/0000000f-423f-7000-8000-000000000000", 404}, {path + "?version=bad", 400}, {path + "?version=99", 404}} {
		if w := authzCall(h, "owner", "GET", tc.path, ""); w.Code != tc.code {
			t.Fatalf("invalid read: %d %s", w.Code, w.Body.String())
		}
	}
}
