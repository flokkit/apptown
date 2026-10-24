package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func workflowAttachmentRequest(f workflowHTTPFixture, dataDir string, method string, changeID ids.ID, name string, body []byte) *httptest.ResponseRecorder {
	path := fmt.Sprintf("/api/v1/changes/%d/attachments", changeID)
	var payload bytes.Buffer
	if method == "POST" {
		mw := multipart.NewWriter(&payload)
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write(body)
		_ = mw.Close()
		r := httptest.NewRequest(method, path, &payload).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(changeID))
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		NewAttachments(f.DB, dataDir, nil).UploadAttachment(w, r)
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		return w
	}
	if name != "" {
		path += "/" + name
	}
	r := httptest.NewRequest(method, path, nil).WithContext(f.Context)
	r.SetPathValue("id", fmt.Sprint(changeID))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	h := NewAttachments(f.DB, dataDir, nil)
	switch method {
	case "GET":
		if name == "" {
			h.ListAttachments(w, r)
		} else {
			h.GetAttachment(w, r)
		}
	case "DELETE":
		h.DeleteAttachment(w, r)
	}
	return w
}

func TestAttachmentUploadListGetDelete(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	dataDir := t.TempDir()
	body := []byte("id,name\n1,forge\n")
	w := workflowAttachmentRequest(f, dataDir, "POST", f.ChangeID, "data.csv", body)
	if w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var up struct {
		Name   string `json:"name"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	if up.Name != "data.csv" || up.Bytes != len(body) || up.SHA256 == "" {
		t.Fatalf("upload metadata: %+v", up)
	}
	attachments := workflow.GitWorkspace{DataDir: dataDir}
	if b, err := os.ReadFile(filepath.Join(attachments.AttachmentDir(f.ChangeID), "data.csv")); err != nil || !bytes.Equal(b, body) {
		t.Fatalf("canonical copy: %v", err)
	}
	if names := attachments.AttachmentNames(f.ChangeID); len(names) != 1 || names[0] != "data.csv" {
		t.Fatalf("attachment names: %v", names)
	}
	w = workflowAttachmentRequest(f, dataDir, "GET", f.ChangeID, "", nil)
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listed []struct {
		Name  string `json:"name"`
		Bytes int64  `json:"bytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "data.csv" || listed[0].Bytes != int64(len(body)) {
		t.Fatalf("list payload: %+v", listed)
	}
	w = workflowAttachmentRequest(f, dataDir, "GET", f.ChangeID, "data.csv", nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("get: %d %q", w.Code, w.Body.String())
	}
	if w = workflowAttachmentRequest(f, dataDir, "GET", f.ChangeID, ".hidden", nil); w.Code != 400 {
		t.Fatalf("bad name: %d", w.Code)
	}
	// An attachment is never replaced in place; removing it is the one way
	// its bytes go, and that needs its request on the record.
	if w = workflowAttachmentRequest(f, dataDir, "POST", f.ChangeID, "data.csv", []byte("other")); w.Code != 409 {
		t.Fatalf("a second upload of the name: %d %s", w.Code, w.Body.String())
	}
	restore := f.DB.FailNextStatement("-- name: AppendReceipt")
	w = workflowAttachmentRequest(f, dataDir, "DELETE", f.ChangeID, "data.csv", nil)
	restore()
	if w.Code != 503 {
		t.Fatalf("a delete whose request was not recorded: %d %s", w.Code, w.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(attachments.AttachmentDir(f.ChangeID), "data.csv")); err != nil || !bytes.Equal(b, body) {
		t.Fatalf("the attachment's bytes after a refused replace and delete: %q %v", b, err)
	}
	w = workflowAttachmentRequest(f, dataDir, "DELETE", f.ChangeID, "data.csv", nil)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if got, err := audit.Recent(t.Context(), f.DB, audit.Filter{ProjectID: f.ProjectID, KindPrefix: "change.attachment-remove"}); err != nil || len(got) != 2 || got[0].Kind != "change.attachment-removed" || got[1].Kind != "change.attachment-remove-requested" {
		t.Fatalf("a removal's request and outcome: %+v %v", got, err)
	}
	if names := attachments.AttachmentNames(f.ChangeID); len(names) != 0 {
		t.Fatalf("survived delete: %v", names)
	}
	if _, err := os.Stat(filepath.Join(attachments.AttachmentDir(f.ChangeID), "data.csv")); !os.IsNotExist(err) {
		t.Fatalf("canonical copy survived: %v", err)
	}
}

func TestAttachmentUploadGuards(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	shipped, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "shipped"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DB.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(f.Context, tx, f.ProjectID, shipped.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	if w := workflowAttachmentRequest(f, dataDir, "POST", shipped.ID, "late.txt", []byte("x")); w.Code != 409 {
		t.Fatalf("shipped upload: %d %s", w.Code, w.Body.String())
	}
	if w := workflowAttachmentRequest(f, dataDir, "POST", idstest.N(999999), "x.txt", []byte("x")); w.Code != 404 {
		t.Fatalf("unknown change: %d", w.Code)
	}
	if w := workflowAttachmentRequest(f, dataDir, "POST", f.ChangeID, ".secret", []byte("x")); w.Code != 400 {
		t.Fatalf("dotfile: %d %s", w.Code, w.Body.String())
	}
}

func TestAttachmentLimitRejectsOverwriteWithoutLosingExistingFile(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	dir := t.TempDir()
	if w := workflowAttachmentRequest(f, dir, "POST", f.ChangeID, "notes.txt", []byte("keep this")); w.Code != 201 {
		t.Fatalf("initial upload: %d %s", w.Code, w.Body.String())
	}
	w := workflowAttachmentRequest(f, dir, "POST", f.ChangeID, "notes.txt", bytes.Repeat([]byte("x"), maxAttachmentBytes+1))
	if w.Code != 413 {
		t.Fatalf("oversize: %d %s", w.Code, w.Body.String())
	}
	w = workflowAttachmentRequest(f, dir, "GET", f.ChangeID, "notes.txt", nil)
	if w.Code != 200 || w.Body.String() != "keep this" {
		t.Fatalf("rejected overwrite lost data: %d %q", w.Code, w.Body.String())
	}
}
