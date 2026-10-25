package deployment

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
)

func TestLegacyScopeAssociationPreservesOwnedReceiptsAtomically(t *testing.T) {
	s, _, write := providerAssociationWorld(t)
	effects := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			effects++
			t.Errorf("association attempted remote effect %s %s", r.Method, r.URL)
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"docker","Type":1}]`)
		case "/api/endpoints/3":
			fmt.Fprint(w, `{"Id":3,"Name":"docker","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(w, `{"ID":"engine-a"}`)
		case "/api/stacks":
			fmt.Fprint(w, `[{"Id":4,"Name":"legacy-app","EndpointId":3}]`)
		case "/api/stacks/4/file":
			fmt.Fprint(w, `{"StackFileContent":"services: {}\n"}`)
		default:
			t.Errorf("unexpected observation %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	write(server.URL, "key")
	target, err := s.Targets.Put(t.Context(), &Target{Name: "legacy", Kind: KindPortainer, Status: TargetReady, Address: map[string]any{"urlFrom": "PT_URL", "apiKeyFrom": "PT_KEY", AddressEnvironment: "docker"}})
	if err != nil {
		t.Fatal(err)
	}
	project := createTestProject(t, s.db, "legacy-owned", "Legacy")
	dep, err := s.Deployments.EnsureStanding(t.Context(), project, "lan", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []deploy.Owned{{Kind: "stack", ID: "4", Name: "legacy-app"}, {Kind: "volume", ID: "kept-data", Name: "kept-data", Data: true, Need: "postgres"}} {
		if err := s.Deployments.OwnerOf(dep).Created(t.Context(), o); err != nil {
			t.Fatal(err)
		}
	}
	q := deploymentsql.New(s.db.SQL())
	before, err := q.OwnedByDeployment(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.InspectProviderAssociation(t.Context(), target.Name, ProviderAssociationSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Resources) != 1 {
		t.Fatalf("stack association: %+v", preview.Resources)
	}
	selection := ProviderAssociationSelection{ExpectedRevision: preview.Revision, Expected: &preview.Scope, Resources: preview.Resources}
	dbtest.FailNextStatement(t, s.db, "UPDATE targets SET connection_id")
	if _, err := s.AssociateProvider(t.Context(), target.Name, selection); err == nil {
		t.Fatal("injected target-write failure hidden")
	}
	current, err := s.Targets.ByID(t.Context(), target.ID)
	if err != nil || !current.ConnectionID.IsZero() || len(current.ProviderScope) != 0 {
		t.Fatalf("partial target association: %+v %v", current, err)
	}
	currentDep, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || !reflect.DeepEqual(currentDep.Handle, dep.Handle) {
		t.Fatalf("partial resource association: %+v %v", currentDep, err)
	}
	bindings, err := s.env.integrations.Bindings.All(t.Context())
	if err != nil || len(bindings) != 0 {
		t.Fatalf("partial connection: %+v %v", bindings, err)
	}
	if _, err := s.AssociateProvider(t.Context(), target.Name, selection); err != nil {
		t.Fatal(err)
	}
	after, err := q.OwnedByDeployment(t.Context(), dep.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("association rewrote creation receipts: before=%+v after=%+v err=%v", before, after, err)
	}
	if effects != 0 {
		t.Fatalf("association performed %d remote writes", effects)
	}
}
