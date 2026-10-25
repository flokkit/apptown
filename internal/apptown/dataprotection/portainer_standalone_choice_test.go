package dataprotection

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/openrouter/openrouterfake"
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

func TestDefaultPortainerReleaseRemovalRevokesOnlyItsIssuedKey(t *testing.T) {
	w := newDataWorldWith(t, dataWorldOptions{contract: []byte("app:\n  web: {build: ., public: true, ports: [\"8080\"]}\nneeds:\n  llm: {}\n")})
	type stack struct {
		ID       int64  `json:"Id"`
		Name     string `json:"Name"`
		Endpoint int64  `json:"EndpointId"`
		Compose  string `json:"-"`
	}
	var mu sync.Mutex
	stacks := map[int64]stack{}
	refuseDelete := true
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(out, `[{"Id":3,"Name":"customers","Type":1}]`)
		case "/api/endpoints/3":
			fmt.Fprint(out, `{"Id":3,"Name":"customers","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(out, `{"ID":"customer-engine"}`)
		case "/api/endpoints/3/docker/version":
			fmt.Fprint(out, `{"Os":"linux","Arch":"amd64"}`)
		case "/api/stacks":
			rows := make([]stack, 0, len(stacks))
			for _, st := range stacks {
				rows = append(rows, st)
			}
			json.NewEncoder(out).Encode(rows)
		case "/api/stacks/create/standalone/string":
			var body struct {
				Name    string `json:"name"`
				Compose string `json:"stackFileContent"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				out.WriteHeader(400)
				return
			}
			id := int64(len(stacks) + 1)
			stacks[id] = stack{ID: id, Name: body.Name, Endpoint: 3, Compose: body.Compose}
			fmt.Fprintf(out, `{"Id":%d}`, id)
		default:
			raw, ok := strings.CutPrefix(r.URL.Path, "/api/stacks/")
			if !ok {
				t.Errorf("unexpected provider request %s %s", r.Method, r.URL)
				http.Error(out, "unexpected", 500)
				return
			}
			idText, file := strings.CutSuffix(raw, "/file")
			id, err := strconv.ParseInt(idText, 10, 64)
			st, exists := stacks[id]
			if err != nil || !exists {
				http.NotFound(out, r)
				return
			}
			if file && r.Method == "GET" {
				json.NewEncoder(out).Encode(map[string]string{"StackFileContent": st.Compose})
				return
			}
			if r.Method == "DELETE" {
				if refuseDelete {
					http.Error(out, "provider refused deletion", http.StatusServiceUnavailable)
					return
				}
				delete(stacks, id)
				out.WriteHeader(204)
				return
			}
			t.Errorf("unexpected stack request %s %s", r.Method, r.URL)
			http.Error(out, "unexpected", 500)
		}
	}))
	defer server.Close()
	keys := openrouterfake.New(llmProvisioningSentinel)
	defer keys.Close()
	for name, value := range map[string]string{"CUSTOMER_URL": server.URL, "CUSTOMER_KEY": "fixture", "OPENROUTER_PROVISIONING_KEY": llmProvisioningSentinel} {
		if err := w.s.secrets.SetBootstrap(t.Context(), name, value, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.integrations.Bindings.Save(t.Context(), "openrouter", integration.CapLLM, "openrouter", `{"url":"`+keys.Base+`","provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":5,"previewLimit":1}`, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := w.integrations.Attachments.Attach(t.Context(), w.project, integration.CapLLM, "openrouter", "{}", integration.Entered, "alice", nil); err != nil {
		t.Fatal(err)
	}
	target, err := w.s.AttachPortainerTarget(t.Context(), deployment.PortainerAttachment{Name: "customer-platform", EndpointID: 3, Environment: "customers", URLFrom: "CUSTOMER_URL", APIKeyFrom: "CUSTOMER_KEY", Domain: "customers.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.Targets.Offer(t.Context(), target, "*", deployment.OfferCaps{}, "alice"); err != nil {
		t.Fatal(err)
	}
	bundle, err := w.s.Bundles.Cut(t.Context(), w.project, w.bundle.ContractRevision, map[string]string{"web": releaseRepo + digestA}, nil, w.contract, digestOrigin(&claimedAttempt{db: w.db, project: w.project, run: idstest.N(8), step: idstest.N(80), at: 1}))
	if err != nil {
		t.Fatal(err)
	}
	deps := make([]*deployment.Deployment, 2)
	ownedKeys := make([]deploy.Owned, 2)
	for i, name := range []string{"customer-a", "customer-b"} {
		deps[i], err = w.s.Deployments.EnsureStanding(t.Context(), w.project, name, target.ID, deployment.Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"})
		if err != nil {
			t.Fatal(err)
		}
		var accepted deployment.AcceptedRelease
		if i == 0 {
			selected, selectErr := w.s.SetReleaseDefault(t.Context(), w.project, deps[i].ID, 0)
			if selectErr != nil {
				t.Fatal(selectErr)
			}
			accepted, err = w.s.RequestDefaultRelease(t.Context(), deployment.DefaultReleaseSelection{ProjectID: w.project, ExpectedRevision: &selected.Revision})
		} else {
			accepted, err = w.s.RequestRelease(t.Context(), deployment.DeploySelection{ProjectID: w.project, DeploymentName: name, BundleID: bundle})
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := w.s.ExecuteRelease(t.Context(), accepted.ID); err != nil {
			t.Fatal(err)
		}
		owned, err := w.s.Deployments.Owned(t.Context(), deps[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range owned {
			if o.Kind == deployment.KindOpenRouterKey {
				ownedKeys[i] = o
			}
		}
		if ownedKeys[i].ID == "" {
			t.Fatal("successful release did not record issued key")
		}
	}
	if ownedKeys[0].ID == ownedKeys[1].ID || keys.CallsOf("POST") != 2 {
		t.Fatal("customer releases shared an issued key")
	}
	if _, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: deps[0].Name}); err == nil {
		t.Fatal("provider deletion refusal reported removal")
	}
	for i, dep := range deps {
		owned, err := w.s.Deployments.Owned(t.Context(), dep.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, o := range owned {
			found = found || o.ID == ownedKeys[i].ID
		}
		if !found {
			t.Fatal("failed removal lost registered key ownership")
		}
	}
	if keys.CallsOf("DELETE") != 0 {
		t.Fatal("failed provider deletion revoked an owned key")
	}
	mu.Lock()
	refuseDelete = false
	mu.Unlock()
	removed, err := w.s.TakeDown(t.Context(), RemoveDeployment{ProjectID: w.project, Name: deps[0].Name})
	if err != nil || removed.Removed == nil {
		t.Fatalf("default deployment removal: %+v %v", removed, err)
	}
	if keys.CallsOf("DELETE") != 1 || len(keys.Keys()) != 1 {
		t.Fatalf("revocation was not scoped: deletes=%d remaining=%d", keys.CallsOf("DELETE"), len(keys.Keys()))
	}
	owned, err := w.s.Deployments.Owned(t.Context(), deps[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range owned {
		found = found || o.ID == ownedKeys[1].ID
	}
	if !found {
		t.Fatal("surviving deployment lost key ownership")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stacks) != 1 {
		t.Fatalf("removal changed another stack: %+v", stacks)
	}
	for _, st := range stacks {
		if !strings.Contains(st.Compose, deps[1].ID.String()) {
			t.Fatal("remaining stack is not second deployment")
		}
	}
}
