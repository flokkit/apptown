package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
)

func TestErrorReportingPageReadsStoredStatusAndKeepsTheLastSummary(t *testing.T) {
	t.Parallel()
	s, id, dir := runtimeFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("ERRORS_KEY=private-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`[{"title":"crash","count":"1"}]`))
	}))
	t.Cleanup(srv.Close)
	config, _ := json.Marshal(map[string]any{"url": srv.URL, "org": "team", "tokenFrom": []string{"ERRORS_KEY"}})
	if _, err := s.Bindings.Save(t.Context(), "errors", CapErrors, "glitchtip", string(config), "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.attach(t.Context(), id, CapErrors, "errors", `{"providerProject":"demo"}`, Provisioned, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	p, err := project.ByIDIn(t.Context(), s.Bindings.db, id)
	if err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		t.Helper()
		out := map[string]any{}
		if err := s.ErrorsIssueSummary(t.Context(), &p, out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := read(); out["issuesError"] == nil || calls.Load() != 0 {
		t.Fatal("cold page checked the provider", out, calls.Load())
	}
	if err := s.observeErrorReport(t.Context(), &p); err != nil {
		t.Fatal(err)
	}
	if out := read(); len(out["unresolved"].([]map[string]string)) != 1 || calls.Load() != 1 {
		t.Fatal("missing stored summary", out, calls.Load())
	}
	fail.Store(true)
	if err := s.observeErrorReport(t.Context(), &p); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the service, as a restart does, and read without a provider call.
	s = NewService(s.Bindings.db, Dependencies{Secrets: s.secrets})
	if out := read(); out["issuesError"] == nil || len(out["unresolved"].([]map[string]string)) != 1 || calls.Load() != 2 {
		t.Fatal("failure erased the summary", out, calls.Load())
	}
}
