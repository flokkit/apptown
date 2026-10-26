package docker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

type collisionSecrets map[string]map[string]string

func (s collisionSecrets) Resolve(context.Context, string, string, string, *appcontract.Contract, func(context.Context) error) (Delivery, error) {
	return Delivery{Env: func(consumer string) map[string]string { return s[consumer] }}, nil
}

func TestDockerRefusesNeedSecretCollisionsBeforeApplicationEffects(t *testing.T) {
	for _, consumer := range []string{"web", "worker", "init-migrate"} {
		t.Run(consumer, func(t *testing.T) {
			c, err := appcontract.Parse([]byte("app:\n  web: {image: 'app:1', ports: ['8080'], public: true}\n  worker: {image: 'app:1'}\ninit:\n  - {name: migrate, image: 'app:1'}\nneeds: {postgres: {}}\n"))
			if err != nil {
				t.Fatal(err)
			}
			deps := Dependencies{Needs: credentialNeed{password: "need-sentinel"}, Secrets: collisionSecrets{consumer: {"DATABASE_URL": "manual-sentinel"}}, StaticServer: testStaticServer, DFImage: testDF}
			client := &recordingClient{}
			_, err = NewHost(deps).Boot(t.Context(), BootRequest{Docker: Scope{Client: client}, Name: "owned", Workspace: t.TempDir()}, c)
			if err == nil || !strings.Contains(err.Error(), consumer) || !strings.Contains(err.Error(), "DATABASE_URL") || strings.Contains(err.Error(), "sentinel") || len(client.calls) != 0 {
				t.Fatalf("ambiguous boot: %v calls%v", err, client.calls)
			}
			if _, err := PrepareBoot(t.Context(), deps, nil, "app", "preview", t.TempDir(), c); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
				t.Fatalf("prepared ambiguous snapshot: %v", err)
			}
			// Old retained snapshots are refused without changing their recorded bytes.
			p := Prepared{Version: PreparedVersion, Contract: c, NeedEnv: map[string]string{"DATABASE_URL": "need-sentinel"}, Secrets: map[string]map[string]string{consumer: {"DATABASE_URL": "manual-sentinel"}}}
			before, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewHost(p.Dependencies(upProbe{}, testImages())).Boot(t.Context(), BootRequest{Docker: Scope{Client: client}, Name: "owned", Workspace: t.TempDir(), Scratch: t.TempDir()}, c)
			if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
				t.Fatalf("retained ambiguous boot not refused: %v", err)
			}
			after, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("retained request changed")
			}
		})
	}
}
