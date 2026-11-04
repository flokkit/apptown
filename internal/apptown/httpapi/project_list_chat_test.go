package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
)

type projectChatRead struct {
	available map[ids.ID]bool
	err       error
}

func (s projectChatRead) ChangeChatAvailable(_ context.Context, id ids.ID) (bool, error) {
	return s.available[id], s.err
}

// Both list shapes carry the conversation owner's current availability, per
// project. A failed owner read must not masquerade as chat being unavailable.
func TestProjectListReportsOwnerChatAvailability(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	authority := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := authority.SeedAdministrator(t.Context(), "reader"); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: testAdmittedPrincipal(t, authority, "reader"), Receipt: "reader"})
	yes := httpTestProject(t, db, "chat", "Chat")
	no := httpTestProject(t, db, "form", "Form")
	projects := project.New(db, authority)
	checked := access.Checked{Authority: authority, Projects: projects}
	chats := projectChatRead{available: map[ids.ID]bool{yes: true, no: false}}
	h := NewProjectViews(projects,
		workflow.New(t.Context(), db, workflow.Dependencies{Authority: checked}),
		adrs.New(db, checked, nil, nil),
		deployment.New(t.Context(), db, deployment.Dependencies{Projects: projects, Authority: checked}),
		execution.Runner{}, nil, t.TempDir(), chats, authority)
	for _, path := range []string{"/projects", "/projects?limit=5"} {
		w := httptest.NewRecorder()
		h.ListProjects(w, httptest.NewRequest("GET", path, nil).WithContext(ctx))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		// Decode raw booleans to distinguish an absent field from false.
		var rows []struct {
			ID   ids.ID `json:"id"`
			Chat *bool  `json:"chatAvailable"`
		}
		if path == "/projects" {
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
		} else {
			var page struct {
				Projects json.RawMessage `json:"projects"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(page.Projects, &rows); err != nil {
				t.Fatal(err)
			}
		}
		if len(rows) != 2 {
			t.Fatalf("rows: %s", w.Body.String())
		}
		for _, row := range rows {
			if row.Chat == nil || *row.Chat != chats.available[row.ID] {
				t.Fatalf("project %s availability: %v", row.ID, row.Chat)
			}
		}
	}
	h.chats = projectChatRead{err: errors.New("availability failed")}
	w := httptest.NewRecorder()
	h.ListProjects(w, httptest.NewRequest("GET", "/projects", nil).WithContext(ctx))
	if w.Code != 500 {
		t.Fatalf("failed owner read: %d %s", w.Code, w.Body.String())
	}
}
