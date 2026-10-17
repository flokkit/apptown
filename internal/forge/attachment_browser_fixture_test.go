package forge

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"flokkit.io/forge/internal/ids"
)

// att_seedChange creates a project and begins a real Change, returning its id.
func att_seedChange(t *testing.T, a *App) ids.ID {
	t.Helper()
	projectID := createReadTestProject(t, a, "p")
	id := createReadTestChange(t, a, projectID, "attachment fixture")
	if _, err := a.Work.Begin(asPerson("bls"), id); err != nil {
		t.Fatal(err)
	}
	return id
}

// att_upload posts a multipart form with a single file field.
func att_upload(a *App, changeID ids.ID, filename string, body []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(body)
	_ = mw.Close()
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/changes/%d/attachments", changeID), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Forge-Actor", "bls")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

func att_json(a *App, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Forge-Actor", "bls")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}
