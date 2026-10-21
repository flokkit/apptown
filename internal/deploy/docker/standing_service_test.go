package docker

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
)

// An existing service with changed declared parameters is recreated only when
// its data is synthetic. Protected data keeps its service and receives a refusal.
func TestStandingServiceIsReconciledAgainstTheContract(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	client.reply = func(req Command) ([]byte, error) {
		args := req.Args
		if len(args) > 2 && args[0] == "inspect" {
			for _, x := range args {
				if strings.Contains(x, "Config.Env") {
					return []byte("POSTGRES_USER=tps\nPOSTGRES_DB=tps\nPOSTGRES_PASSWORD=old\n"), nil
				}
				if strings.Contains(x, "Config.Image") {
					return []byte("postgres:16\n"), nil
				}
			}
		}
		return nil, nil
	}
	dk := Scope{Client: client}
	svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:16", Env: map[string]string{"POSTGRES_USER": "app", "POSTGRES_DB": "app", "POSTGRES_PASSWORD": "new"}}
	if d, err := ServiceDrift(context.Background(), dk, "apptown-svc-env-x-main-postgres", svc); err != nil || strings.Join(d, ",") != "POSTGRES_DB,POSTGRES_PASSWORD,POSTGRES_USER" {
		t.Fatalf("drift: %v", d)
	}
	same := deploy.ServiceSpec{Name: "postgres", Image: "postgres:16", Env: map[string]string{"POSTGRES_USER": "tps", "POSTGRES_DB": "tps", "POSTGRES_PASSWORD": "old"}}
	if d, err := ServiceDrift(context.Background(), dk, "apptown-svc-env-x-main-postgres", same); err != nil || len(d) != 0 {
		t.Fatalf("no drift expected: %v", d)
	}
	// Non-synthetic data, or data of no known class: refused, nothing removed.
	for _, class := range []string{"production", "non-production", ""} {
		if _, err := EnsureStandingServices(context.Background(), dk, "env-x-main", []deploy.ServiceSpec{svc}, class, Ownership{}); err == nil || !strings.Contains(err.Error(), "will not replace") {
			t.Fatalf("%q data was replaced: %v", class, err)
		}
	}
	if hasStandingCall(client.args, "rm -f apptown-svc-env-x-main-postgres") {
		t.Fatal("the production service was removed")
	}
	// Synthetic: recreated from the contract.
	if _, err := EnsureStandingServices(context.Background(), dk, "env-x-main", []deploy.ServiceSpec{svc}, "synthetic", Ownership{}); err != nil {
		t.Fatal(err)
	}
	if !hasStandingCall(client.args, "rm -f apptown-svc-env-x-main-postgres") || !hasStandingCall(client.args, "run -d") {
		t.Fatalf("the synthetic service was not recreated: %v", client.args)
	}
}

func hasStandingCall(calls [][]string, prefix string) bool {
	for _, args := range calls {
		if strings.HasPrefix(strings.Join(args, " "), prefix) {
			return true
		}
	}
	return false
}
