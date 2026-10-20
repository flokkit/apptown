package forge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec"
)

// Real routing must refuse before the repository operation, and admitted
// choices must affect Git and retain the deciding actor in the audit receipt.
func TestRepositoryIntegrityHTTPChoices(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "trusted-header", AuthHeader: "X-Test-User", AuthProxySecret: "test-proxy"})
	a.SeedAdministrators(context.Background(), []string{"bls"})
	request := func(who, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Test-User", who)
		req.Header.Set("X-AppTown-Proxy-Secret", "test-proxy")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		return w
	}
	pid := createReadTestProject(t, a, "integrity")
	p, err := a.Projects.BySlug(context.Background(), "integrity")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), &p, "main", ""); err != nil {
		t.Fatal(err)
	}
	repo := a.projectRepoPath(p.Slug)
	file := filepath.Join(repo, "sample.txt")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("original\n")
	stageAll(repo)
	if _, err := gitexec.Run(t.Context(), repo, "commit", "-m", "initial sample"); err != nil {
		t.Fatal(err)
	}
	// The commit above was made outside the forge: the project says main is
	// ahead of the record, as a note, without naming who moved it.
	standing := func() (state, note string) {
		t.Helper()
		w := request("member", "GET", "/api/v1/projects/integrity", "")
		var body struct {
			Main struct{ State, Note string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
			t.Fatalf("project: %d %s", w.Code, w.Body.String())
		}
		return body.Main.State, body.Main.Note
	}
	if state, note := standing(); state != "ahead" || !strings.HasPrefix(note, "main is 1 commit ahead of AppTown's record at ") {
		t.Fatalf("main standing after a commit outside AppTown: %q %q", state, note)
	}
	path := "/api/v1/projects/integrity/repo/integrity"
	write("outside edit\n")
	before, _ := gitexec.Run(t.Context(), repo, "rev-parse", "HEAD")
	for _, tc := range []struct {
		who, body string
		want      int
	}{{"member", `{"action":"adopt"}`, 403}, {"bls", `{"action":"invent"}`, 400}} {
		w := request(tc.who, "POST", path, tc.body)
		if w.Code != tc.want {
			t.Fatalf("refusal %s: %d %s", tc.who, w.Code, w.Body.String())
		}
		after, _ := gitexec.Run(t.Context(), repo, "rev-parse", "HEAD")
		data, _ := os.ReadFile(file)
		if before != after || string(data) != "outside edit\n" {
			t.Fatal("refused operation changed repository")
		}
	}
	w := request("bls", "POST", path, `{"action":"adopt"}`)
	if w.Code != 200 {
		t.Fatalf("adopt: %d %s", w.Code, w.Body.String())
	}
	committed, _ := gitexec.Run(t.Context(), repo, "show", "HEAD:sample.txt")
	if committed != "outside edit" {
		t.Fatalf("not committed: %q", committed)
	}
	wantMainRecorded(t, a, "integrity", scm.ViaIntegrityAdopt)
	if state, note := standing(); state != "recorded" || note != "" {
		t.Fatalf("main standing after keeping the edits: %q %q", state, note)
	}
	// A hub that cannot be read is said so, never shown as nothing: here the
	// recorded project's hub has gone, and another project has a default
	// branch but no hub and no record (what the backfill leaves unknown).
	if err := os.Rename(repo, repo+".gone"); err != nil {
		t.Fatal(err)
	}
	if state, note := standing(); state != "unreadable" || note != "could not read main" {
		t.Fatalf("main standing with the hub gone: %q %q", state, note)
	}
	if err := os.Rename(repo+".gone", repo); err != nil {
		t.Fatal(err)
	}
	createReadTestProject(t, a, "lost")
	lost, err := a.Projects.BySlug(context.Background(), "lost")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Projects.SetDefaultBranch(context.Background(), lost.ID, "main"); err != nil {
		t.Fatal(err)
	}
	lw := request("member", "GET", "/api/v1/projects/lost", "")
	var lostBody struct{ Main *struct{ State, Note string } }
	if err := json.Unmarshal(lw.Body.Bytes(), &lostBody); err != nil || lw.Code != 200 || lostBody.Main == nil || lostBody.Main.State != "unreadable" || lostBody.Main.Note != "could not read main" {
		t.Fatalf("a project with a default branch and no hub: %d %s", lw.Code, lw.Body.String())
	}
	var payload struct {
		OK     bool
		Action string
		Files  []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || !payload.OK || payload.Action != "adopt" || len(payload.Files) != 1 {
		t.Fatalf("adopt response: %s", w.Body.String())
	}
	write("discard me\n")
	w = request("bls", "POST", path, `{"action":"discard"}`)
	if w.Code != 200 {
		t.Fatalf("discard: %d %s", w.Code, w.Body.String())
	}
	data, _ := os.ReadFile(file)
	if string(data) != "outside edit\n" {
		t.Fatalf("discarded tracked edits: %q", data)
	}
	w = request("bls", "POST", path, `{"action":"discard"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"clean":true`) {
		t.Fatalf("clean: %d %s", w.Code, w.Body.String())
	}
	byBLS := 0
	for _, entry := range ledgerEntries(t, a, pid, "integrity.out-of-band-edits") {
		if entry.Actor == "bls" {
			byBLS++
		}
	}
	if byBLS != 2 {
		t.Fatalf("out-of-band edit receipts by bls: %d", byBLS)
	}
}
