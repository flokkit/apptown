package scm

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/fault"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/ids"
)

type attachmentAuthority struct{}

func (attachmentAuthority) Require(_ context.Context, p access.Principal, _ access.Permission) (access.Principal, error) {
	return p, nil
}

// A failed response or initial push must not cause a second remote create.
// These failures occur on opposite sides of the durable provider-result write.
func TestAttachmentRetryKeepsUnknownAndIdentifiedResults(t *testing.T) {
	for _, outcome := range []string{"unknown", "identified"} {
		t.Run(outcome, func(t *testing.T) {
			db := testDB(t)
			ctx := t.Context()
			now := time.Now()
			pid, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
				return project.CreateAt(ctx, tx, now, project.Spec{Slug: "attachment", Name: "Attachment"})
			})
			if err != nil {
				t.Fatal(err)
			}
			projects := project.New(db, nil)
			if err := projects.SetDefaultBranch(ctx, pid, "main"); err != nil {
				t.Fatal(err)
			}
			p, err := projects.ByID(ctx, pid)
			if err != nil {
				t.Fatal(err)
			}
			dataDir := t.TempDir()
			repo, err := p.Repository(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(repo, 0700); err != nil {
				t.Fatal(err)
			}
			if err := InitializeRepository(ctx, repo, "main"); err != nil {
				t.Fatal(err)
			}
			bindings := integration.NewWithClock(db, nil)
			if _, err := bindings.Save(ctx, "gitlab", integration.CapSCM, "gitlab", `{}`, "fixture"); err != nil {
				t.Fatal(err)
			}
			binding, err := bindings.ByName(ctx, "gitlab")
			if err != nil {
				t.Fatal(err)
			}
			var creates atomic.Int64
			var base string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v4/projects" && r.Method == "POST" {
					creates.Add(1)
					if outcome == "unknown" {
						http.Error(w, "result lost", 500)
						return
					}
				}
				if r.URL.Path == "/api/v4/projects" || r.URL.Path == "/api/v4/projects/71" {
					fmt.Fprintf(w, `{"id":71,"path_with_namespace":"team/independent","http_url_to_repo":%q}`, base+"/team/independent.git")
					return
				}
				http.Error(w, "initial push unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			base = server.URL
			client, err := gitlab.New(base, "snapshot")
			if err != nil {
				t.Fatal(err)
			}
			scope := ProviderScope{ConnectionID: binding.ID, Kind: "gitlab", Base: base, AccountID: 41}
			provider := &identityProvider{session: ProviderSession{Scope: scope, Client: client}}
			owner := NewRepositoryAttachments(db, AttachmentDependencies{Projects: projects, Authority: access.Checked{Authority: attachmentAuthority{}, Projects: projects}, Provider: provider, Locks: &RepositoryLocks{}, DataDir: dataDir})
			ctx = access.WithCaller(ctx, access.Caller{Principal: access.Principal{ID: ids.New(now), Kind: "user", Name: "fixture"}, Verified: true, Receipt: "fixture"})
			request := AttachmentRequest{ProjectID: pid, RequestID: ids.New(now), Create: true, NamespaceID: 3, RepositoryName: "independent"}
			for range 2 {
				if _, err := owner.Attach(ctx, request); err == nil {
					t.Fatal("failed external effect reported attached")
				}
			}
			if outcome == "unknown" {
				if _, err := owner.Push(ctx, pid, "main"); err == nil || errors.Is(err, ErrNoUpstream) || fault.KindOf(err) != fault.Conflict {
					t.Fatalf("durable unknown was treated as absent upstream: %v", err)
				}
			}
			if creates.Load() != 1 {
				t.Fatalf("retry created %d provider projects", creates.Load())
			}
			intent, found, err := owner.readIntent(ctx, pid)
			if err != nil || !found {
				t.Fatalf("lost durable intent: %v %v", found, err)
			}
			wantPhase := "dispatched"
			if outcome == "identified" {
				wantPhase = "identified"
			}
			if intent.row.Phase != wantPhase {
				t.Fatalf("phase=%s want=%s", intent.row.Phase, wantPhase)
			}
			if outcome == "identified" && intent.result.ID != 71 {
				t.Fatal("lost provider object after initial push failed")
			}
			if _, found, err := readAttachment(ctx, db, pid); err != nil || found {
				t.Fatalf("invented completed attachment: %v %v", found, err)
			}
			if receipts := ledger(t, db, "project.remote.attached"); len(receipts) != 0 {
				t.Fatal("failed push produced success receipt")
			}
			assertRepositoryConnectionRetained(t, bindings, binding.ID)
			after, found, err := owner.readIntent(ctx, pid)
			if err != nil || !found || after.row.ConnectionID != binding.ID {
				t.Fatalf("pending attachment lost original connection: %+v %v", after.row, err)
			}
			request.RepositoryName = "different"
			if _, err := owner.Attach(ctx, request); err == nil || creates.Load() != 1 {
				t.Fatal("changed request caused provider create")
			}
		})
	}
}

// A verified existing origin has an attachment but no create intent. Its
// connection must remain just as strongly owned as an unfinished create.
func TestRepositoryAttachmentRetainsItsConnection(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	pid, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "existing", Name: "Existing"})
	})
	if err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, nil)
	if err := projects.SetDefaultBranch(ctx, pid, "main"); err != nil {
		t.Fatal(err)
	}
	p, err := projects.ByID(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	repo, err := p.Repository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := InitializeRepository(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	bindings := integration.NewWithClock(db, nil)
	if _, err := bindings.Save(ctx, "gitlab", integration.CapSCM, "gitlab", `{}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	binding, err := bindings.ByName(ctx, "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected provider effect %s", r.Method)
			http.Error(w, "unexpected effect", 500)
			return
		}
		fmt.Fprintf(w, `{"id":71,"path_with_namespace":"team/existing","http_url_to_repo":%q}`, base+"/team/existing.git")
	}))
	defer server.Close()
	base = server.URL
	client, err := gitlab.New(base, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	provider := &identityProvider{session: ProviderSession{Scope: ProviderScope{ConnectionID: binding.ID, Kind: "gitlab", Base: base, AccountID: 41}, Client: client}}
	owner := NewRepositoryAttachments(db, AttachmentDependencies{Projects: projects, Provider: provider, Locks: &RepositoryLocks{}, DataDir: dir})
	if _, err := gitexec.HubRun(ctx, repo, "remote", "add", "origin", base+"/team/existing.git"); err != nil {
		t.Fatal(err)
	}
	verified, err := owner.Resolve(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := owner.readIntent(ctx, pid); err != nil || found {
		t.Fatalf("existing repository invented an intent: %v %v", found, err)
	}
	assertRepositoryConnectionRetained(t, bindings, binding.ID)
	after, found, err := readAttachment(ctx, db, pid)
	if err != nil || !found || after != verified.Identity {
		t.Fatalf("attachment identity changed during attempted retirement: %+v %v", after, err)
	}
}

func assertRepositoryConnectionRetained(t *testing.T, bindings *integration.Bindings, connectionID ids.ID) {
	t.Helper()
	if err := bindings.Delete(t.Context(), "gitlab", "fixture"); !errors.Is(err, integration.ErrAttached) {
		t.Fatalf("repository connection retirement was not refused: %v", err)
	}
	retained, err := bindings.ByName(t.Context(), "gitlab")
	if err != nil || retained.ID != connectionID {
		t.Fatalf("connection identity disappeared: %+v %v", retained, err)
	}
	// Saving the same display name after refused deletion updates the original
	// connection; it cannot manufacture a replacement UUID behind its consumers.
	if _, err := bindings.Save(t.Context(), "gitlab", integration.CapSCM, "gitlab", `{"description":"updated"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	retained, err = bindings.ByName(t.Context(), "gitlab")
	if err != nil || retained.ID != connectionID {
		t.Fatalf("same-name save substituted connection identity: %+v %v", retained, err)
	}
}
