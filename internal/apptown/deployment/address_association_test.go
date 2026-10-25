package deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestLegacyAddressConfirmationPreservesProviderEvidenceWithoutRemoteWrites(t *testing.T) {
	s, authority, write := providerAssociationWorld(t)
	s.configuration.Authority = authority
	s.configuration.Projects = &requestSources{}
	var calls atomic.Int32
	var body atomic.Value
	body.Store("")
	var stackName atomic.Value
	stackName.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("association performed remote write %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-API-Key") != "address-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"docker","Type":1}]`)
		case "/api/endpoints/3":
			fmt.Fprint(w, `{"Id":3,"Name":"docker","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(w, `{"ID":"address-engine"}`)
		case "/api/stacks":
			_ = json.NewEncoder(w).Encode([]portainer.Stack{{ID: 4, Name: stackName.Load().(string), EndpointID: 3}})
		case "/api/stacks/4/file":
			_ = json.NewEncoder(w).Encode(map[string]string{"StackFileContent": body.Load().(string)})
		default:
			t.Errorf("unexpected provider request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	write(server.URL, "address-key")
	target, err := s.AttachPortainerTarget(t.Context(), PortainerAttachment{Name: "address-legacy", URLFrom: "PT_URL", APIKeyFrom: "PT_KEY", Environment: "docker", Domain: "new.apps.example"})
	if err != nil {
		t.Fatal(err)
	}
	project := createTestProject(t, s.db, "legacy-address", "Legacy address")
	dep, err := s.Deployments.EnsureStanding(t.Context(), project, "lan", target.ID, Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	prefix, base := portainer.StackNames(dep.ID)
	stackName.Store(portainer.StackName(prefix, base))
	original := deploy.IntranetCompose(deploy.IntranetStack{Secrets: secret.Delivery{}, Prefix: prefix, Base: base, Hosts: []string{"Old.apps.example", "Customer.example"}, Domain: "apps.example", Image: "registry.example/app@sha256:123", Port: 8080})
	marked, err := portainer.MarkCompose(original, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	body.Store(marked)
	handle, _ := json.Marshal(map[string]string{"stack": stackName.Load().(string)})
	// sql-guarantee: emulate a restored legacy serving handle; confirmation must not clear the restore barrier.
	if _, err := s.db.Exec(`UPDATE deployments SET handle_json=$2,restore_check='unconfirmed' WHERE id=$1`, dep.ID, handle); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: only upgrade creates historical unverified address evidence.
	if _, err := s.db.Exec(`INSERT INTO deployment_address_settings(deployment_id,verification_state,legacy_evidence_json) VALUES($1,'legacy-unverified','{"projectReleaseDomain":"Customer.example"}')`, dep.ID); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	authority.deny = errors.New("revoked")
	if _, err := s.InspectAddress(t.Context(), project, dep.ID); !errors.Is(err, authority.deny) {
		t.Fatalf("authority refusal: %v", err)
	}
	if calls.Load() != before {
		t.Fatal("provider observed before current authority")
	}
	authority.deny = nil
	observed, err := s.InspectAddress(t.Context(), project, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Ingress.URL != "https://Customer.example" || observed.Ingress.Hosts[0] != "Old.apps.example" || observed.Ingress.CertificateDomain != "apps.example" {
		t.Fatalf("observation recomputed old routing: %+v", observed)
	}
	body.Store(strings.ReplaceAll(marked, "Customer.example", "changed.example"))
	if _, err := s.ConfirmAddress(t.Context(), project, dep.ID, observed); err == nil {
		t.Fatal("changed provider observation accepted")
	}
	body.Store(marked)
	dbtest.FailNextStatement(t, s.db, "-- name: AppendReceipt")
	if _, err := s.ConfirmAddress(t.Context(), project, dep.ID, observed); err == nil {
		t.Fatal("failed confirmation receipt hidden")
	}
	reading, err := s.ReadAddress(t.Context(), project, dep.ID)
	if err != nil || reading.Verified || reading.Revision != 1 || len(reading.ReservedHosts) != 0 {
		t.Fatalf("failed receipt committed address: %+v %v", reading, err)
	}
	confirmed, err := s.ConfirmAddress(t.Context(), project, dep.ID, observed)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Revision != 2 || !confirmed.Ingress.Same(observed.Ingress) {
		t.Fatalf("confirmation changed routing: %+v", confirmed)
	}
	reading, err = s.ReadAddress(t.Context(), project, dep.ID)
	if err != nil || !reading.Verified || reading.GeneratedHostname != "Old.apps.example" || len(reading.ReservedHosts) != 2 || !strings.Contains(string(reading.LegacyEvidence), "Customer.example") {
		t.Fatalf("lost evidence/reservations: %+v %v", reading, err)
	}
	dep, err = s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || dep.RestoreCheck != RestoreUnconfirmed {
		t.Fatalf("confirmation lifted restore hold: %+v %v", dep, err)
	}
	foreign, err := portainer.MarkCompose(original, idstest.N(918))
	if err != nil {
		t.Fatal(err)
	}
	body.Store(foreign)
	if _, err := s.InspectAddress(t.Context(), project, dep.ID); err == nil {
		t.Fatal("foreign stack ingress accepted")
	}
}
