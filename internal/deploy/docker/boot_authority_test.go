package docker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type authoritySecrets struct{ after func() }

func (s authoritySecrets) Resolve(ctx context.Context, slug, deployment, workspace string, contract *appcontract.Contract, allowed func(context.Context) error) (Delivery, error) {
	delivery, err := (liveSecrets{}).Resolve(ctx, slug, deployment, workspace, contract, allowed)
	s.after()
	return delivery, err
}

func TestBootRechecksAuthorityAfterPreparation(t *testing.T) {
	for _, phase := range []string{"current", "before boot", "after secrets", "after fill"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			contract, err := appcontract.Parse([]byte(preparedContract))
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			if err := os.WriteFile(workspace+"/Dockerfile", []byte("FROM scratch\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			denied := phase == "before boot"
			refusal := errors.New("requester's permission withdrawn")
			client := &recordingClient{}
			resolved, filled := false, false
			deps := Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}, Probe: upProbe{},
				Secrets: authoritySecrets{after: func() {
					resolved = true
					if phase == "after secrets" {
						denied = true
					}
				}}}
			_, err = NewHost(deps).Boot(t.Context(), BootRequest{Docker: Scope{Client: client}, Name: "apptown-preview-change-1", StackID: "pvsnap-change-1", Workspace: workspace, Slug: "app", Deployment: "preview", Probe: time.Second, Scratch: t.TempDir(),
				Allowed: func(context.Context) error {
					if denied {
						return refusal
					}
					return nil
				},
				fill: func(string) error {
					filled = true
					if phase == "after fill" {
						denied = true
					}
					return nil
				}}, contract)
			calls := strings.Join(client.calls, "\n")
			appStarted := false
			for _, call := range client.calls {
				if strings.HasPrefix(call, "run ") && strings.Contains(call, "--name apptown-preview-change-1 ") {
					appStarted = true
				}
			}
			if phase == "current" {
				if err != nil || !resolved || !filled || !appStarted {
					t.Fatalf("control did not complete boot: resolved=%t filled=%t app=%t err=%v\n%s", resolved, filled, appStarted, err, calls)
				}
				return
			}
			if !errors.Is(err, refusal) {
				t.Fatalf("permission denial lost: %v\n%s", err, calls)
			}
			if appStarted {
				t.Fatalf("withdrawn permission started app:\n%s", calls)
			}
			switch phase {
			case "before boot":
				if resolved || filled || len(client.calls) != 0 {
					t.Fatalf("denied boot performed work: resolved=%t filled=%t\n%s", resolved, filled, calls)
				}
			case "after secrets":
				if !resolved || filled {
					t.Fatalf("secret-phase control invalid: resolved=%t filled=%t", resolved, filled)
				}
				for _, call := range client.calls {
					if strings.HasPrefix(call, "run ") || strings.HasPrefix(call, "build ") {
						t.Errorf("denied preparation reached Docker effect: %s", call)
					}
				}
			case "after fill":
				if !filled {
					t.Fatal("revocation point not reached")
				}
				for _, call := range client.calls {
					if strings.Contains(call, Current.Init("pvsnap-change-1", "migrate")) {
						t.Errorf("denied fill continued into initialization: %s", call)
					}
				}
			}
		})
	}
}

func TestBootRequiresAnExplicitAuthorityPolicy(t *testing.T) {
	t.Parallel()
	client := &recordingClient{}
	// Omission must fail before resolving configuration or touching Docker.
	_, err := NewHost(Dependencies{}).Boot(t.Context(), BootRequest{Docker: Scope{Client: client}}, &appcontract.Contract{})
	var boot *BootError
	if !errors.As(err, &boot) || boot.Phase != PhaseResolve || !strings.Contains(err.Error(), "authority check") || len(client.calls) != 0 {
		t.Fatalf("implicit authority: err=%v calls=%v", err, client.calls)
	}
	if err := AuthorisedWhenRequested(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestDriverRequiresAnExplicitBootAuthorityPolicy(t *testing.T) {
	t.Parallel()
	// A valid source still cannot reach checkpointing or Docker without a policy.
	body := []byte(`{}`)
	art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)), Source: &deploy.Source{Contract: &appcontract.Contract{}}}
	calls := 0
	d := Driver{ServingLabel: "stable", Scope: func(context.Context) (Scope, error) { calls++; return Scope{}, errors.New("scope reached") }}
	_, err := d.Reconcile(t.Context(), deploy.Placement{ID: idstest.N(1), Name: "main", Label: "1"}, 1, art, "key", nil, func(map[string]any) error { calls++; return nil })
	if err == nil || !strings.Contains(err.Error(), "authority check") || calls != 0 {
		t.Fatalf("implicit driver authority: calls=%d err=%v", calls, err)
	}
}
