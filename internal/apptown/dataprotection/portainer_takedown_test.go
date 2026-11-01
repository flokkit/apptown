package dataprotection

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
)

// asyncEdge is a fake Portainer whose one environment is an edge agent in
// async mode: Portainer gives it no Docker proxy. Its stack can still be
// deleted through Portainer's own API.
type asyncEdge struct {
	mu      sync.Mutex
	deleted []string
}

func (e *asyncEdge) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch {
		case r.URL.Path == "/api/endpoints":
			fmt.Fprint(w, `[{"Id":5,"Name":"lan","Type":4,"Edge":{"AsyncMode":true}}]`)
		case r.Method == "GET" && r.URL.Path == "/api/stacks/12/file":
			fmt.Fprint(w, `{"StackFileContent":"services: {}\nvolumes:\n  apptown-need-ledger-postgres: {}\n"}`)
		case r.Method == "DELETE" && r.URL.Path == "/api/stacks/12":
			e.deleted = append(e.deleted, r.URL.Path)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	})
}

// A legacy async-edge deployment cannot establish the standalone daemon scope
// required for provider effects. Even an explicit no-copy choice cannot grant
// identity: both the stack and its recorded holdings remain untouched.
func TestAnUnverifiedAsyncEdgeRefusesEvenExplicitNoCopy(t *testing.T) {
	t.Parallel()
	w := newDataWorld(t)
	s, pid := w.s, w.project
	edge := &asyncEdge{}
	srv := httptest.NewServer(edge.handler(t))
	t.Cleanup(srv.Close)
	secrets := filepath.Join(s.dataDir, "secrets.env")
	held, err := os.ReadFile(secrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secrets, append(held, []byte("EDGE_URL="+srv.URL+"\nEDGE_KEY=edge-key\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	target, err := s.Targets.Put(ctx, &deployment.Target{Name: "edge", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, NeedsMode: "emulators", Status: deployment.TargetReady,
		Address: map[string]any{"urlFrom": "EDGE_URL", "apiKeyFrom": "EDGE_KEY", "environment": "lan"}})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(ctx, pid, "lan", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	owner := s.Deployments.OwnerOf(dep)
	for _, o := range []deploy.Owned{{Kind: "stack", ID: "12", Name: "apptown-ledger"}, {Kind: "volume", ID: "apptown-ledger_apptown-need-ledger-postgres", Name: "apptown-ledger_apptown-need-ledger-postgres", Data: true, Need: "postgres"}} {
		if err := owner.Created(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.TakeBackup(ctx, pid, "lan", "bls"); !errors.Is(err, ErrDataNotKnown) || !strings.Contains(err.Error(), "explicit provider association") {
		t.Fatalf("a backup there: %v", err)
	}
	for _, choice := range []string{"", "lan"} {
		if _, err := s.TakeDown(ctx, RemoveDeployment{ProjectID: pid, Name: "lan", WithoutFinalCopy: choice}); err == nil {
			t.Fatalf("unverified teardown accepted no-copy choice %q", choice)
		}
	}
	if len(edge.deleted) != 0 {
		t.Fatal("unverified teardown deleted the stack")
	}
	if _, err := s.Deployments.ByID(ctx, dep.ID); err != nil {
		t.Fatalf("refusal removed deployment: %v", err)
	}
}
