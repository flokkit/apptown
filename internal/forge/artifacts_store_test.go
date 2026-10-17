package forge

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// HTTP preserves text and encodes binary bodies without losing any bytes.
// Workflow owns the independent body/body_bytes representation assertions.
func TestArtifactHTTPPreservesTextAndBinary(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	router, err := newRouteMux(a, appRoutes(a))
	if err != nil {
		t.Fatal(err)
	}
	projectID := createReadTestProject(t, a, "p")
	changeID := createReadTestChange(t, a, projectID, "artifacts")
	run := startReadTestRun(t, a, changeID, schema.Stage{Name: "work", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}})
	png := "\x89PNG\r\n\x1a\n\x00\x00binary\xff\xfe"
	textID, err := a.Work.AttachArtifact(asPerson("bls"), run.Run.ID, workflow.ArtifactDraft{Kind: "report", Name: "REPORT.md", Body: "# hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	binID, err := a.Work.AttachArtifact(asPerson("bls"), run.Run.ID, workflow.ArtifactDraft{Kind: "file", Name: "logo.png", Body: png})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		actor  string
		id     ids.ID
		body   string
		binary bool
	}{{"text", "anonymous", textID, "# hello\n", false}, {"binary", "anonymous", binID, png, true}, {"binary named user", "bls", binID, png, true}} {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequestAs(httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+tc.id.String(), nil), tc.actor)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("artifact: %d %s", rec.Code, rec.Body)
			}
			var out struct {
				Binary     bool   `json:"binary"`
				BodyBase64 string `json:"bodyBase64"`
				Body       string `json:"body"`
				BodyBytes  int    `json:"bodyBytes"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			got := out.Body
			if tc.binary {
				decoded, err := base64.StdEncoding.DecodeString(out.BodyBase64)
				if err != nil || out.Body != "" {
					t.Fatalf("binary encoding: %+v %v", out, err)
				}
				got = string(decoded)
			} else if out.BodyBase64 != "" {
				t.Fatalf("text unexpectedly encoded: %+v", out)
			}
			if out.Binary != tc.binary || got != tc.body || out.BodyBytes != len(tc.body) {
				t.Fatalf("served form: %+v", out)
			}
		})
	}
}
