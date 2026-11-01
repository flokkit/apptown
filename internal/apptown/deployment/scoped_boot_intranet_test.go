package deployment

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"gopkg.in/yaml.v3"
)

func TestBootAndIntranetUseScopedPreparedEnvironments(t *testing.T) {
	t.Parallel()
	s, _, client, _, projectID := hostWorld(t, "")
	ws := t.TempDir()
	writeBootFixture(t, ws)
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	c.PublicPart().Env = map[string]string{"PUBLIC_ONLY": "web"}
	c.Parts[1].Env = map[string]string{"WORKER_ONLY": "worker", "DATABASE_URL": "wrong"}
	c.Init[0].Env = map[string]string{"INIT_ONLY": "init", "DATABASE_URL": "wrong"}
	before, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.env.Host(projectID, nil, nil).Boot(context.Background(), docker.BootRequest{Allowed: docker.AuthorisedWhenRequested, Docker: s.hosts.settings.Scope, Name: "apptown-preview-isolation", StackID: "isolation", Workspace: ws, Slug: "p", Probe: time.Second}, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range client.Matching("run ") {
		call := strings.Join(args, " ")
		if strings.Contains(call, "--name apptown-preview-isolation") {
			continue
		}
		if strings.Contains(call, "PUBLIC_ONLY=") || strings.Contains(call, "DATABASE_URL=wrong") {
			t.Fatalf("consumer env leaked or overrode connection: %s", call)
		}
	}
	plan := result.Stack.Plan
	var extras []appcontract.Part
	for _, part := range plan.Parts {
		if !part.Public {
			extras = append(extras, part)
		}
	}
	raw := deploy.IntranetCompose(deploy.IntranetStack{Secrets: secret.Delivery{}, Base: "p", Hosts: []string{"p.example.test"}, Domain: "example.test", Image: "app:1", Port: 8080, Env: plan.PublicPart().Env, Init: plan.Init, Parts: extras, Needs: docker.IntranetNeeds(result.Stack.Needs)})
	var compose struct {
		Services map[string]struct{ Environment map[string]string }
	}
	if err := yaml.Unmarshal([]byte(raw), &compose); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker", "init-migrate"} {
		env := compose.Services[name].Environment
		if env["PUBLIC_ONLY"] != "" || env["DATABASE_URL"] == "" || env["DATABASE_URL"] == "wrong" {
			t.Fatalf("intranet consumer %s: %v", name, env)
		}
	}
	after, err := json.Marshal(c)
	if err != nil || string(after) != string(before) {
		t.Fatal("boot changed declaration")
	}
	if !reflect.DeepEqual(plan.Init[0].Command, c.Init[0].Command) {
		t.Fatal("init argv changed")
	}
}
