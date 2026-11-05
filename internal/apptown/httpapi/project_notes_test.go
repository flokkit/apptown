package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func TestProjectNotesReadMainAndChangeShowsProposedLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := newWorkflowHTTPFixtureAt(t, dir)
	p, err := f.Projects.BySlug(f.Context, "shop")
	if err != nil {
		t.Fatal(err)
	}
	hub, err := p.Repository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hub, 0755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, hub, "init", "-b", "main")
	gittest.Run(t, hub, "config", "user.name", "test")
	gittest.Run(t, hub, "config", "user.email", "test@example.test")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(hub, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() { t.Helper(); gittest.Run(t, hub, "add", "-A"); gittest.Run(t, hub, "commit", "-m", "project") }
	write("README.md", "project")
	commit()
	if err := f.Projects.SetDefaultBranch(f.Context, f.ProjectID, "main"); err != nil {
		t.Fatal(err)
	}
	read := func() ProjectOverviewView {
		t.Helper()
		r := httptest.NewRequest("GET", "/", nil).WithContext(f.Context)
		r.SetPathValue("slug", "shop")
		w := httptest.NewRecorder()
		NewProjectOverview(f.Projects, f.Work, dir).ProjectOverview(w, r)
		var got ProjectOverviewView
		if w.Code != 200 {
			t.Fatalf("overview: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); got.Notes == nil || got.Notes.Present || len(got.Notes.Notes) != 0 || got.Notes.Error != "" {
		t.Fatalf("absent: %+v", got.Notes)
	}
	old := "- Real photos only. (agent · change 1)"
	write("PROJECT-NOTES.md", "# Project notes\n"+old+"\n")
	commit()
	main := gittest.Run(t, hub, "rev-parse", "HEAD")
	ch, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Update notes"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Work.PrepareWorkspace(f.Context, ch.ID); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, hub, "checkout", "-b", ch.Branch)
	added := "- Credit every photo. (Sam · change 2)"
	write("PROJECT-NOTES.md", "# Project notes\n"+added+"\nmalformed proposal\n")
	commit()
	ws := (workflow.Layout{DataDir: dir}).ChangeWorkspace(ch.ID)
	gittest.Run(t, ws, "fetch", hub, ch.Branch)
	gittest.Run(t, ws, "merge", "--ff-only", "FETCH_HEAD")
	got := read()
	if got.Notes == nil || got.Notes.Commit != main || len(got.Notes.Notes) != 1 || got.Notes.Notes[0].Author != "agent" || got.Notes.Notes[0].ChangeID != f.ChangeID {
		t.Fatalf("main notes: %+v", got.Notes)
	}
	r := httptest.NewRequest("GET", "/", nil).WithContext(f.Context)
	r.SetPathValue("id", ch.ID.String())
	w := httptest.NewRecorder()
	NewChangeDetail(f.Work, adrs.New(f.DB, access.Checked{Authority: identity.Scoped(f.Access, "adrs"), Projects: f.Projects}, nil, nil), deployment.New(r.Context(), f.DB, deployment.Dependencies{}), f.DB, usage.New(f.DB), dir).GetChange(w, r)
	var detail ChangeCockpit
	if w.Code != 200 {
		t.Fatalf("detail: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.NotesDiff == nil || len(detail.NotesDiff.Added) != 2 || detail.NotesDiff.Added[0] != added || detail.NotesDiff.Removed[0] != old {
		t.Fatalf("proposed notes: %+v comparison=%+v head=%s branch=%s", detail.NotesDiff, detail.CommittedDiff, detail.Head, ch.Branch)
	}
	gittest.Run(t, hub, "checkout", "main")
	write("PROJECT-NOTES.md", "malformed landed file")
	commit()
	if got := read(); got.Notes.Error == "" {
		t.Fatal("invalid notes presented as empty")
	}
}
