package digitalocean

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// heldRecorder answers a need's recorded handle and keeps what it is asked
// to record.
type heldRecorder struct {
	held      map[string]any
	requested map[string]any
}

func (r *heldRecorder) Recorded(context.Context, ids.ID, string) (map[string]any, error) {
	return r.held, nil
}

func (r *heldRecorder) RecordIntent(_ context.Context, _ ids.ID, _ string, requested map[string]any) error {
	r.requested = requested
	return nil
}

func (r *heldRecorder) RecordIdentity(context.Context, ids.ID, string, map[string]any, string, string) error {
	return nil
}

// clusterAPI is the provider for one online cluster: it answers by id and
// records the requests, so a test sees whether a cluster was looked for by
// name or made.
func clusterAPI(t *testing.T, id, name string) (*httptest.Server, *[]string, *[]map[string]any) {
	var mu sync.Mutex
	var calls []string
	var made []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		cluster := `{"id":"` + id + `","name":"` + name + `","engine":"pg","version":"16","status":"online","tags":[]}`
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/databases/"+id:
			_, _ = w.Write([]byte(`{"database":` + cluster + `}`))
		case r.Method == "GET" && r.URL.Path == "/v2/databases":
			_, _ = w.Write([]byte(`{"databases":[]}`))
		case r.Method == "POST" && r.URL.Path == "/v2/databases":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			made = append(made, body)
			mu.Unlock()
			n, _ := body["name"].(string)
			_, _ = w.Write([]byte(`{"database":{"id":"` + id + `","name":"` + n + `","engine":"pg","version":"16","status":"online"}}`))
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v2/databases/"):
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(server.Close)
	return server, &calls, &made
}

func clusterDriver(server *httptest.Server, rec deploy.NeedRecorder) *Driver {
	return NewDriver(Dependencies{
		Client: func(context.Context) (*Client, Config, error) {
			return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd"}, nil
		},
		Slug: "demo", Needs: rec,
	}, Options{OwnDatabase: func(context.Context, string, string, string) error { return nil }})
}

// A need made before 1.0 keeps its cluster: fetched by the id its record
// holds and asked for under the name it was made with, so the record's
// identity holds, nothing is looked up by a new name and nothing is made.
func TestAnEstablishedNeedKeepsTheClusterItsRecordNames(t *testing.T) {
	t.Parallel()
	server, calls, made := clusterAPI(t, "db-9", "forge-demo-main")
	rec := &heldRecorder{held: map[string]any{"id": "db-9", "name": "forge-demo-main", "database": "app", "user": "app"}}
	_, handle, err := clusterDriver(server, rec).FulfilNeed(context.Background(), deploy.Placement{ID: idstest.N(7), Name: "main"}, "postgres", nil, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if handle["id"] != "db-9" || handle["name"] != "forge-demo-main" || rec.requested["name"] != "forge-demo-main" {
		t.Fatalf("the established cluster was not kept: handle %v, asked %v", handle, rec.requested)
	}
	if len(*made) != 0 || slices.Contains(*calls, "GET /v2/databases") {
		t.Fatalf("a cluster was looked for by name or made: %v", *calls)
	}
}

// A need asked for the first time is named by the deployment's key, and
// tagged with its project and deployment for people.
//
// Kills: a cluster named from the slug and the deployment's name, which two
// deployments can share (shop/eu-main and shop-eu/main).
func TestANewNeedsClusterIsNamedByTheDeploymentsKey(t *testing.T) {
	t.Parallel()
	server, _, made := clusterAPI(t, "db-1", "")
	rec := &heldRecorder{}
	if _, _, err := clusterDriver(server, rec).FulfilNeed(context.Background(), deploy.Placement{ID: shopMain.ID, Name: "main"}, "postgres", nil, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if len(*made) != 1 || (*made)[0]["name"] != "apptown-db-3b89d1da8075" {
		t.Fatalf("made %v", *made)
	}
	tags, _ := (*made)[0]["tags"].([]any)
	if len(tags) != 3 || tags[0] != "apptown" || tags[1] != "apptown-project-demo" || tags[2] != "apptown-deployment-main" {
		t.Fatalf("tags %v", tags)
	}
}
