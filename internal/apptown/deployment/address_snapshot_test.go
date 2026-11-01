package deployment

import (
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestAddressSnapshotKeepsExactIngressAndRefusesMalformedPersistence(t *testing.T) {
	original := AddressSnapshot{DeploymentID: idstest.N(1), TargetID: idstest.N(2), Revision: 3, Ingress: deploy.Ingress{Hosts: []string{"app.apps.example", "customer.example"}, URL: "https://customer.example", CertificateDomain: "apps.example"}}
	raw, err := addressSnapshotJSON(&original)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := addressSnapshotFrom(raw)
	if err != nil || !original.same(*restored) {
		t.Fatalf("snapshot roundtrip: %+v %v", restored, err)
	}
	restored.Ingress.Hosts[0] = "caller.apps.example"
	if original.Ingress.Hosts[0] != "app.apps.example" {
		t.Fatal("decoded snapshot shares caller memory")
	}
	if absent, err := addressSnapshotFrom(nil); err != nil || absent != nil {
		t.Fatalf("missing historical snapshot fabricated: %+v %v", absent, err)
	}
	for _, bad := range []string{"null", "{}", `{"deploymentId":"bad"}`, `{"revision":3,"ingress":{"hosts":["https://not-a-host"]}}`} {
		if _, err := addressSnapshotFrom([]byte(bad)); err == nil {
			t.Fatalf("malformed snapshot %s accepted", bad)
		}
	}
	if (Inputs{}).same(Inputs{Address: &original}) {
		t.Fatal("absent ingress equals admitted ingress")
	}
}
