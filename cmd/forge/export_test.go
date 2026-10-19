package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// `forge export-project` writes the file the forge sent and reports its
// hash and counts apart from it; a file whose bytes are not the ones the
// forge hashed is refused, never presented as the export, and a refusal is
// the forge's own sentence.
func TestExportProjectWritesTheFileAndChecksItsHash(t *testing.T) {
	body := []byte("the move file")
	sum := sha256.Sum256(body)
	header := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects/busy/export" {
			rw.WriteHeader(409)
			_, _ = rw.Write([]byte(`{"error":"the project cannot be exported now: change 7 (t) is open"}`))
			return
		}
		rw.Header().Set("X-Forge-Export-Sha256", header)
		rw.Header().Set("X-Forge-Export-Counts", `{"counts":{"changes":2,"chats":3},"emptyChats":1,"skipped":[{"kind":"change","id":"00000000-0008-7000-8000-000000000000","title":"try blue","state":"abandoned"}],"skippedChats":1}`)
		_, _ = rw.Write(body)
	}))
	defer srv.Close()
	prev := api
	api = &client{base: srv.URL, actor: "tester", http: srv.Client(), wait: func(time.Duration) {}, getenv: func(string) string { return "" }}
	defer func() { api = prev }()

	var out, report bytes.Buffer
	if err := exportProject("app", &out, &report); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), body) || !strings.Contains(report.String(), header) || !strings.Contains(report.String(), "chats 3 (1 empty, not moved)") || !strings.Contains(report.String(), "sensitive") || !strings.Contains(report.String(), "only an AppTown running this same build reads it") || !strings.Contains(report.String(), "change 00000000-0008-7000-8000-000000000000 (abandoned): try blue") {
		t.Fatalf("file %q, report %q", out.String(), report.String())
	}
	header = strings.Repeat("0", 64)
	if err := exportProject("app", &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "export again") {
		t.Fatalf("a damaged file was accepted: %v", err)
	}
	if err := exportProject("busy", &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "change 7") {
		t.Fatalf("the refusal: %v", err)
	}
}
