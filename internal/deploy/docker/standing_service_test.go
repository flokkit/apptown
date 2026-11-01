package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
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
	if d, err := serviceDrift(context.Background(), dk, "apptown-svc-env-x-main-postgres", svc); err != nil || strings.Join(d, ",") != "POSTGRES_DB,POSTGRES_PASSWORD,POSTGRES_USER" {
		t.Fatalf("drift: %v", d)
	}
	same := deploy.ServiceSpec{Name: "postgres", Image: "postgres:16", Env: map[string]string{"POSTGRES_USER": "tps", "POSTGRES_DB": "tps", "POSTGRES_PASSWORD": "old"}}
	if d, err := serviceDrift(context.Background(), dk, "apptown-svc-env-x-main-postgres", same); err != nil || len(d) != 0 {
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

// Container loss must not let a newly requested engine open retained files.
func TestStandingEngineWithoutContainerRefusesRetainedData(t *testing.T) {
	t.Parallel()
	const stack = "env-retained-main"
	vol := Current.NeedVolume(stack, "postgres")
	svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:17-alpine", KeepsImage: true, ConfigAtStart: true}
	own := Ownership{Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}}
	for _, class := range []string{"production", "synthetic", ""} {
		for _, unreadable := range []bool{false, true} {
			t.Run(class+map[bool]string{false: "/retained", true: "/unreadable"}[unreadable], func(t *testing.T) {
				h := newOwnedHost("network "+Current.Network(stack), "volume "+vol)
				h.unknown["volume "+vol] = unreadable
				_, err := EnsureStandingServices(t.Context(), h.scope(), stack, []deploy.ServiceSpec{svc}, class, own)
				if err == nil || !strings.Contains(err.Error(), vol) {
					t.Fatalf("retained/unknown volume admitted: %v", err)
				}
				if len(h.runs) != 0 || len(h.created) != 0 || len(h.removed) != 0 {
					t.Fatalf("refusal wrote resources: runs=%v created=%v removed=%v", h.runs, h.created, h.removed)
				}
			})
		}
	}
	h := newOwnedHost("network " + Current.Network(stack))
	if _, err := EnsureStandingServices(t.Context(), h.scope(), stack, []deploy.ServiceSpec{svc}, "production", own); err != nil {
		t.Fatal(err)
	}
	if len(h.runs) != 1 || !h.objects["volume "+vol] {
		t.Fatalf("new storage did not start: %+v", h)
	}
}

func TestStandingEngineWithContainerKeepsExistingLifecycle(t *testing.T) {
	const stack = "env-witnessed-main"
	vol := Current.NeedVolume(stack, "postgres")
	h := newOwnedHost("network "+Current.Network(stack), "volume "+vol, "container "+Current.Service(stack, "postgres"))
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		line := strings.Join(req.Args, " ")
		switch {
		case strings.Contains(line, ".HostConfig.Memory"):
			return []byte("0 0"), nil
		case strings.Contains(line, ".Config.Image"):
			return []byte("postgres:16-alpine"), nil
		case strings.Contains(line, ".State.Running"):
			return []byte("true"), nil
		}
		return h.reply(req)
	}}
	svc := deploy.ServiceSpec{Name: "postgres", Image: "postgres:16-alpine", KeepsImage: true}
	if _, err := EnsureStandingServices(t.Context(), Scope{Client: client}, stack, []deploy.ServiceSpec{svc}, "production", Ownership{Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}}); err != nil {
		t.Fatal(err)
	}
	if len(h.runs) != 0 || len(h.created) != 0 || len(h.removed) != 0 {
		t.Fatalf("existing service replaced: %+v", h)
	}
}
