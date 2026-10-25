package dataprotection

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
)

func TestVerifiedStandaloneWithoutCopyKeepsDataAfterExplicitChoice(t *testing.T) {
	w := newDataWorld(t)
	var deploymentID string
	deleted := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/endpoints/5":
			fmt.Fprint(out, `{"Id":5,"Name":"standalone","Type":1}`)
		case r.Method == "GET" && r.URL.Path == "/api/endpoints/5/docker/info":
			fmt.Fprint(out, `{"ID":"verified-engine"}`)
		case r.Method == "GET" && r.URL.Path == "/api/endpoints/5/docker/version":
			http.Error(out, "copy API unavailable", http.StatusServiceUnavailable)
		case r.Method == "GET" && r.URL.Path == "/api/stacks":
			fmt.Fprint(out, `[{"Id":12,"Name":"apptown-ledger","EndpointId":5}]`)
		case r.Method == "GET" && r.URL.Path == "/api/stacks/12/file":
			json.NewEncoder(out).Encode(map[string]string{"StackFileContent": "x-apptown-deployment: " + deploymentID + "\nservices: {}\nvolumes:\n  apptown-need-ledger-postgres: {}\n"})
		case r.Method == "DELETE" && r.URL.Path == "/api/stacks/12":
			deleted++
			out.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected provider effect/request %s %s", r.Method, r.URL)
			http.Error(out, "unexpected", 500)
		}
	}))
	defer server.Close()
	file := filepath.Join(w.s.dataDir, "secrets.env")
	held, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(held, []byte("STANDALONE_URL="+server.URL+"\nSTANDALONE_KEY=key\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	target, err := w.s.AttachPortainerTarget(t.Context(), deployment.PortainerAttachment{Name: "standalone", EndpointID: 5, Environment: "standalone", URLFrom: "STANDALONE_URL", APIKeyFrom: "STANDALONE_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "lan", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	deploymentID = dep.ID.String()
	owner := w.s.Deployments.OwnerOf(dep)
	for _, o := range []deploy.Owned{{Kind: "stack", ID: "12", Name: "apptown-ledger"}, {Kind: "volume", ID: "apptown-ledger_apptown-need-ledger-postgres", Name: "apptown-ledger_apptown-need-ledger-postgres", Data: true, Need: "postgres"}} {
		if err := owner.Created(t.Context(), o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "lan"}); !errors.Is(err, ErrNoFinalCopy) {
		t.Fatalf("no-copy refusal: %v", err)
	}
	if deleted != 0 {
		t.Fatal("unconfirmed teardown deleted stack")
	}
	result, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: "lan", WithoutFinalCopy: "lan"})
	if err != nil || result.Removed == nil || result.Removed.FinalBackup != nil || deleted != 1 {
		t.Fatalf("explicit choice: %+v %v deleted=%d", result, err, deleted)
	}
	retained, err := w.s.Retained.ForProject(t.Context(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range retained {
		why, _ := r.Handle["why"].(string)
		name, _ := r.Handle["name"].(string)
		if name == "apptown-ledger_apptown-need-ledger-postgres" && strings.Contains(why, "no complete final backup") {
			found = true
		}
	}
	if !found {
		t.Fatalf("owned volume not retained: %+v", retained)
	}
}
