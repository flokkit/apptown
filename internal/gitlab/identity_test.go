package gitlab

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProviderIdentityKeepsAccountAndRepositoryObservation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "one-snapshot" {
			t.Errorf("changed credential snapshot")
		}
		switch r.URL.Path {
		case "/api/v4/user":
			_, _ = w.Write([]byte(`{"id":41}`))
		case "/api/v4/projects":
			_, _ = w.Write([]byte(`{"id":71,"path_with_namespace":"team/independent-name","http_url_to_repo":"https://git.example/team/independent-name.git","ssh_url_to_repo":"git@git.example:team/independent-name.git"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, "one-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if id, err := client.AccountID(t.Context()); err != nil || id != 41 {
		t.Fatalf("authenticated identity: %d %v", id, err)
	}
	repo, err := client.CreateRepository(t.Context(), "independent-name", 3, "operation")
	if err != nil || repo.ID != 71 || repo.PathNS != "team/independent-name" || repo.HTTPURL != "https://git.example/team/independent-name.git" || repo.SSHURL != "git@git.example:team/independent-name.git" {
		t.Fatalf("lost provider identity: %+v %v", repo, err)
	}
}

func TestRepositoryURLRefusesCredentialAndProviderAmbiguity(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"https://other.example/gitlab/team/p.git", "http://git.example/gitlab/team/p.git", "https://git.example/team/p.git", "https://git.example/gitlab/../team/p.git", "https://git.example/gitlab/team%2Fp.git", "https://user:password@git.example/gitlab/team/p.git", "https://git.example/gitlab/team/p.git?token=secret", "https://git.example/gitlab/team/p.git#fragment"} {
		if _, _, err := RepositoryURL("https://git.example/gitlab", raw); err == nil {
			t.Errorf("accepted ambiguous repository %s", raw)
		}
	}
	location, path, err := RepositoryURL("https://GIT.example/gitlab/", "https://apptown@git.example/gitlab/team/p.git")
	if err != nil || location != "https://git.example/gitlab/team/p.git" || path != "team/p" {
		t.Fatalf("valid provider location: %s %s %v", location, path, err)
	}
	for _, raw := range []string{"https://user:secret@git.example", "https://git.example/a/../b", "ssh://git.example", "https://git.example?token=x"} {
		if _, err := CanonicalBase(raw); err == nil {
			t.Errorf("accepted provider %s", raw)
		}
	}
}

func TestProviderIdentityAndCreateRefuseRedirectedAuthority(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"account", "create"} {
		t.Run(operation, func(t *testing.T) {
			var foreignCalls atomic.Int64
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				foreignCalls.Add(1)
				_, _ = w.Write([]byte(`{"id":41,"http_url_to_repo":"https://foreign.example/team/p.git"}`))
			}))
			defer foreign.Close()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, foreign.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
			}))
			defer provider.Close()
			client, err := New(provider.URL, "private-token")
			if err != nil {
				t.Fatal(err)
			}
			if operation == "account" {
				_, err = client.AccountID(t.Context())
			} else {
				_, err = client.CreateRepository(t.Context(), "p", 1, "operation")
			}
			if err == nil || foreignCalls.Load() != 0 {
				t.Fatalf("redirect supplied foreign authority or effect: err=%v requests=%d", err, foreignCalls.Load())
			}
		})
	}
}
