package digitalocean

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/digitalocean/dofake"
)

func TestTeardownChecksProviderSelectionAndDeletionOutcome(t *testing.T) {
	for _, scenario := range []string{"matching", "absent", "changed", "pending", "missing-identity", "lookup-error", "delete-error"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			api := &dofake.API{}
			if scenario != "absent" {
				active, pending := "dep-1", ""
				if scenario == "changed" {
					active = "dep-2"
				}
				if scenario == "pending" {
					pending = "dep-2"
				}
				api.SeedApp("app-1", active, pending)
			}
			if scenario == "lookup-error" {
				api.Fail("GET", "/v2/apps/app-1", 503)
			}
			if scenario == "delete-error" {
				api.Fail("DELETE", "/v2/apps/app-1", 503)
			}
			server := httptest.NewServer(api.Handler())
			defer server.Close()
			client := NewClient("test", server.URL)
			d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) { return client, Config{}, nil }}, Options{})
			handle := map[string]any{"appId": "app-1", "deploymentId": "dep-1"}
			if scenario == "missing-identity" {
				delete(handle, "deploymentId")
			}
			_, err := d.Teardown(context.Background(), deploy.Placement{Generation: 1, Handle: handle}, 1, deploy.TakeDown{})
			if (err == nil) != (scenario == "matching" || scenario == "absent") {
				t.Fatalf("teardown: %v", err)
			}
			expected := 0
			if scenario == "matching" || scenario == "delete-error" {
				expected = 1
			}
			state := api.Snapshot()
			if state.DeleteRequests != expected {
				t.Fatalf("deletes %d want %d", state.DeleteRequests, expected)
			}
			for _, request := range state.Requests {
				if request.Path != "/v2/apps/app-1" || (request.Method != "GET" && request.Method != "DELETE") {
					t.Errorf("unexpected provider request: %+v", request)
				}
			}
		})
	}
}

// repoBook is a project's record of the repositories the forge created.
type repoBook struct {
	owned   map[string]bool
	created []string
}

func (b *repoBook) Owns(_ context.Context, registry, repo string) (bool, error) {
	return b.owned[registry+"/"+repo], nil
}
func (b *repoBook) Created(_ context.Context, registry, repo string) error {
	if b.owned == nil {
		b.owned = map[string]bool{}
	}
	b.owned[registry+"/"+repo] = true
	b.created = append(b.created, registry+"/"+repo)
	return nil
}

func repoDriver(t *testing.T, api *dofake.API, book *repoBook) *Driver {
	t.Helper()
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	push := func(_ *Driver, _ context.Context, _ *Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	var owner RepositoryOwner
	if book != nil {
		owner = book
	}
	return NewDriver(Dependencies{Slug: "expense", Repository: owner, Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd", Registry: "forge-test"}, nil
	}}, Options{PushImage: push})
}

// The project's repository is the forge's: the first copy into a registry
// without one makes it and records it; one the forge made is used; one it
// did not make is refused before anything is copied, at preflight too.
func TestTheProjectsRepositoryIsMadeAndRecordedNeverAdopted(t *testing.T) {
	for _, scenario := range []string{"absent", "ours", "someone else's"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			api := &dofake.API{}
			api.SeedRegistry("forge-test", "basic", 5)
			book := &repoBook{}
			switch scenario {
			case "ours":
				api.SeedManifests("expense", "sha256:old")
				book.owned = map[string]bool{"forge-test/expense": true}
			case "someone else's":
				api.SeedManifests("expense", "sha256:theirs")
			}
			d := repoDriver(t, api, book)
			src := deploy.Source{Images: map[string]string{"web": "app:1"}, Contract: &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Image: "app:1", Port: 8080}}}, Revision: "r1"}
			preflight := d.Preflight(context.Background(), deploy.Placement{Name: "cloud"}, src, scopedRenderSecrets{}, func(string, ...any) {})
			_, err := d.Render(context.Background(), deploy.Placement{Name: "cloud"}, src, nil, scopedRenderSecrets{}, func(string, ...any) {})
			if scenario == "someone else's" {
				for _, e := range []error{preflight, err} {
					if e == nil || !deploy.IsTerminal(e) || !strings.Contains(e.Error(), "did not create") {
						t.Fatalf("someone else's repository was used: %v", e)
					}
				}
				if len(book.created) != 0 {
					t.Fatalf("recorded: %v", book.created)
				}
				return
			}
			if preflight != nil || err != nil {
				t.Fatalf("release: %v %v", preflight, err)
			}
			want := map[string][]string{"absent": {"forge-test/expense"}, "ours": nil}[scenario]
			if !slices.Equal(book.created, want) {
				t.Fatalf("recorded %v want %v", book.created, want)
			}
		})
	}
}

// Take-down empties the project's repository only when this was the last
// deployment using it and the forge made it: every image goes, and no
// garbage collection is started (DOCR takes no pushes while one runs).
// Someone else's is never touched; a delete that fails is left, with why.
func TestTheLastTakeDownEmptiesTheForgesRepository(t *testing.T) {
	for _, scenario := range []string{"last", "not last", "not ours", "delete fails"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			api := &dofake.API{}
			api.SeedRegistry("forge-test", "basic", 5)
			api.SeedManifests("expense", "sha256:one", "sha256:two")
			book := &repoBook{owned: map[string]bool{"forge-test/expense": scenario != "not ours"}}
			if scenario == "delete fails" {
				api.Fail("DELETE", "/v2/registry/forge-test/repositories/expense/digests/sha256:two", 503)
			}
			d := repoDriver(t, api, book)
			out, err := d.Teardown(context.Background(), deploy.Placement{Generation: 1}, 1, deploy.TakeDown{LastUser: scenario != "not last"})
			if err != nil {
				t.Fatal(err)
			}
			state := api.Snapshot()
			left := state.RepositoryManifests["expense"]
			switch scenario {
			case "last":
				if len(left) != 0 || state.GarbageCollections != 0 || len(out.Deleted) != 1 || !strings.Contains(out.Deleted[0], "2 images in the image repository forge-test/expense") || len(out.Left) != 0 {
					t.Fatalf("emptied: %v gc %d %+v", left, state.GarbageCollections, out)
				}
			case "not last", "not ours":
				if len(left) != 2 || state.GarbageCollections != 0 || len(out.Deleted) != 0 || len(out.Left) != 0 {
					t.Fatalf("touched: %v gc %d %+v", left, state.GarbageCollections, out)
				}
			case "delete fails":
				if !slices.Equal(left, []string{"sha256:two"}) || len(out.Left) != 1 || out.Left[0].What != "image repository" || !strings.Contains(out.Left[0].Why, "deleted 1 of 2 images") {
					t.Fatalf("failed delete: %v %+v", left, out)
				}
			}
		})
	}
}
