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

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/deployment"
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
			fmt.Fprint(w, `{"StackFileContent":"services: {}\nvolumes:\n  forge-need-ledger-postgres: {}\n"}`)
		case r.Method == "DELETE" && r.URL.Path == "/api/stacks/12":
			e.deleted = append(e.deleted, r.URL.Path)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	})
}

// A deployment in a Portainer environment AppTown cannot reach for its
// data (an async edge agent) reads as not known, and its take-down is
// refused, having changed nothing, unless the person chooses to take it
// down without a final copy: then the stack goes and its volumes stay, each
// named with why.
func TestAPortainerTakeDownWithNoWayToCopyIsAPersonsChoice(t *testing.T) {
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
	target, err := s.Targets.Put(ctx, &deployment.Target{Name: "edge", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, NeedsMode: "emulators", SecretsDelivery: "inject", Status: deployment.TargetReady,
		Address: map[string]any{"urlFrom": "EDGE_URL", "apiKeyFrom": "EDGE_KEY", "environment": "lan"}})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(ctx, pid, "lan", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	owner := s.Deployments.OwnerOf(dep)
	for _, o := range []deploy.Owned{{Kind: "stack", ID: "12", Name: "forge-ledger"}, {Kind: "volume", ID: "forge-ledger_forge-need-ledger-postgres", Name: "forge-ledger_forge-need-ledger-postgres", Data: true, Need: "postgres"}} {
		if err := owner.Created(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.TakeBackup(ctx, pid, "lan", "manual", "bls"); !errors.Is(err, ErrDataNotKnown) || !strings.Contains(err.Error(), "async mode") {
		t.Fatalf("a backup there: %v", err)
	}
	if _, err := s.TakeDown(ctx, RemoveDeployment{ProjectID: pid, Name: "lan"}); !errors.Is(err, ErrNoFinalCopy) || !strings.Contains(err.Error(), "async mode") {
		t.Fatalf("a take-down with no way to copy: %v", err)
	}
	if len(edge.deleted) != 0 {
		t.Fatal("the refused take-down deleted the stack")
	}
	res, err := s.TakeDown(ctx, RemoveDeployment{ProjectID: pid, Name: "lan", WithoutFinalCopy: "lan"})
	if err != nil || res.Removed == nil || res.Removed.FinalBackup != nil || len(edge.deleted) != 1 {
		t.Fatalf("a take-down without a final copy, chosen: %+v %v (stack deleted %v)", res, err, edge.deleted)
	}
	retained, err := s.Retained.ForProject(ctx, pid)
	found := false
	for _, r := range retained {
		why, _ := r.Handle["why"].(string)
		if name, _ := r.Handle["name"].(string); name == "forge-ledger_forge-need-ledger-postgres" && strings.Contains(why, "no complete final backup") {
			found = true
		}
	}
	if err != nil || !found {
		t.Fatalf("the kept volume: %+v %v", retained, err)
	}
}
