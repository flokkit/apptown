package scm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/ids"
)

type identityProvider struct {
	session  ProviderSession
	calls    int
	expected *ProviderScope
}

func (p *identityProvider) Snapshot(_ context.Context, expected *ProviderScope) (ProviderSession, error) {
	p.calls++
	p.expected = expected
	return p.session, nil
}

// Historical evidence must select the saved numeric object even when its old
// path has been reused. No project row or current attachment is consulted.
func TestRecordedRepositoryUsesOriginalProviderObject(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v4/projects/71" || r.Header.Get("PRIVATE-TOKEN") != "retained-snapshot" {
			t.Errorf("wrong recorded identity request: %s token=%t", r.URL.Path, r.Header.Get("PRIVATE-TOKEN") == "retained-snapshot")
			http.Error(w, "wrong identity", 400)
			return
		}
		fmt.Fprintf(w, `{"id":71,"path_with_namespace":"team/moved","http_url_to_repo":%q}`, base+"/team/moved.git")
	}))
	defer server.Close()
	base = server.URL
	client, err := gitlab.New(base, "retained-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	scope := ProviderScope{ConnectionID: ids.New(time.Now()), Kind: "gitlab", Base: base, AccountID: 41}
	identity := RepositoryIdentity{AttachmentID: ids.New(time.Now()), ProjectID: ids.New(time.Now()), Scope: scope, ProviderProjectID: 71, CloneURL: base + "/team/old.git", Path: "team/old"}
	provider := &identityProvider{session: ProviderSession{Scope: scope, Client: client}}
	owner := NewRepositoryAttachments(nil, AttachmentDependencies{Provider: provider})
	verified, err := owner.VerifyRecorded(t.Context(), identity)
	if err != nil || verified.Identity != identity || verified.Observed.PathNS != "team/moved" || verified.Provider.Client != client || calls.Load() != 1 || provider.expected == nil || *provider.expected != scope {
		t.Fatalf("lost historical identity or credential snapshot: %+v %v", verified, err)
	}
	for _, mutate := range []func(*ProviderScope){func(s *ProviderScope) { s.ConnectionID = ids.New(time.Now()) }, func(s *ProviderScope) { s.AccountID++ }, func(s *ProviderScope) { s.Base = "https://different.example" }} {
		provider.session.Scope = scope
		mutate(&provider.session.Scope)
		if _, err := owner.VerifyRecorded(t.Context(), identity); err == nil {
			t.Fatal("accepted changed provider scope")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("changed scope reached provider object lookup")
	}
	provider.session.Scope = scope
	incomplete := identity
	incomplete.AttachmentID = ids.ID{}
	before := provider.calls
	if _, err := owner.VerifyRecorded(t.Context(), incomplete); err == nil || provider.calls != before {
		t.Fatal("legacy identity consulted current credentials")
	}
}
