package digitalocean

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/digitalocean/dofake"
	"flokkit.io/forge/internal/oci"
)

// gatedDriver renders against the fake platform, recording each image copy
// and the credential it was read with.
func gatedDriver(t *testing.T, gateLogin func(string) oci.Credential) (*Driver, *[]oci.Source) {
	t.Helper()
	api := &dofake.API{}
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	var copied []oci.Source
	copyImage := func(_ *Driver, _ context.Context, _ *Client, _, _ string, from oci.Source, _ string, _ func(string, ...any)) (string, error) {
		copied = append(copied, from)
		_, digest, _ := strings.Cut(from.Reference, "@")
		return digest, nil
	}
	push := func(_ *Driver, _ context.Context, _ *Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	d := NewDriver(Dependencies{Slug: "orders", GateLogin: gateLogin, Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd", Registry: "forge-test", InstanceSize: "apps-s-1vcpu-0.5gb"}, nil
	}}, Options{PushImage: push, CopyImage: copyImage})
	return d, &copied
}

const testMarker = "0123456789abcdef0123456789abcdef"

const gateRef = "registry.example/forge/gate@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func gatedContract(t *testing.T) *appcontract.Contract {
	t.Helper()
	c, err := appcontract.Parse([]byte(`app:
  web: {image: app:1, public: true, health: {readiness: {http: /healthz}}}
  worker: {image: worker:1}
init:
  - {name: migrate, image: migrate:1}
`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func renderGated(t *testing.T, d *Driver, g *deploy.Gate, secrets deploy.ConsumerSecrets) (deploy.Artifact, error) {
	return d.Render(context.Background(), deploy.Placement{Name: "copy-3", Gate: g},
		deploy.Source{Images: map[string]string{"web": "app:1", "worker": "worker:1"}, Init: map[string]string{"migrate": "migrate:1"}, Contract: gatedContract(t), Revision: "selected"},
		deploy.NeedBindings{}, secrets, func(string, ...any) {})
}

// A restored copy's app holds production data, so the platform gives the
// gate the only route: the app's public part listens on a private port
// only (its health check there), workers do not start, the gate is the
// only ingress and its health check is TCP, so no anonymous request ever
// reaches the app.
func TestGatedAppHasOnlyTheGatePublic(t *testing.T) {
	t.Parallel()
	d, _ := gatedDriver(t, nil)
	secret := strings.Repeat("ab", 32)
	art, err := renderGated(t, d, &deploy.Gate{Image: gateRef, Secret: secret, Forge: "https://forge.example", Marker: testMarker}, scopedRenderSecrets{"web": {"APP_KEY": "kept"}})
	if err != nil {
		t.Fatal(err)
	}
	var spec AppSpec
	if err := json.Unmarshal(art.Body, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Workers) != 0 {
		t.Fatalf("a worker started behind the gate: %+v", spec.Workers)
	}
	if len(spec.Jobs) != 1 {
		t.Fatalf("the init step did not run before the app: %+v", spec.Jobs)
	}
	if spec.Ingress == nil || len(spec.Ingress.Rules) != 1 || spec.Ingress.Rules[0].Component["name"] != GateComponent {
		t.Fatalf("the ingress does not lead to the gate alone: %+v", spec.Ingress)
	}
	var web, gate *Component
	for i := range spec.Services {
		switch spec.Services[i].Name {
		case "web":
			web = &spec.Services[i]
		case GateComponent:
			gate = &spec.Services[i]
		}
	}
	if web == nil || gate == nil || len(spec.Services) != 2 {
		t.Fatalf("services: %+v", spec.Services)
	}
	if web.HTTPPort != 0 || len(web.InternalPorts) != 1 || web.InternalPorts[0] != 8080 {
		t.Fatalf("the app has a public port or no private one: http_port=%d internal=%v", web.HTTPPort, web.InternalPorts)
	}
	if web.HealthCheck == nil || web.HealthCheck.Port != 8080 || web.HealthCheck.HTTPPath != "/healthz" {
		t.Fatalf("the app's health check is not on its private port: %+v", web.HealthCheck)
	}
	if gate.HTTPPort != deploy.GatePort || gate.HealthCheck == nil || gate.HealthCheck.HTTPPath != "" || gate.HealthCheck.Port != deploy.GatePort {
		t.Fatalf("the gate is not public with a TCP-only health check: %+v", gate)
	}
	if gate.Image.Digest != "sha256:"+strings.Repeat("1", 64) || gate.Image.RegistryType != "DOCR" {
		t.Fatalf("the gate image is not the pinned digest in DOCR: %+v", gate.Image)
	}
	env := func(c *Component) map[string]doEnv {
		m := map[string]doEnv{}
		for _, e := range c.Envs {
			m[e.Key] = e
		}
		return m
	}
	ge, we := env(gate), env(web)
	if ge[deploy.GateEnvSecret].Value != secret || ge[deploy.GateEnvSecret].Type != "SECRET" {
		t.Fatalf("the gate secret is not the gate's encrypted secret: %+v", ge[deploy.GateEnvSecret].Type)
	}
	if ge[deploy.GateEnvUpstream].Value != "http://web:8080" || ge[deploy.GateEnvForge].Value != "https://forge.example" || ge[deploy.GateEnvMarker].Value != testMarker {
		t.Fatalf("gate wiring: %+v", ge)
	}
	for _, c := range append(append([]Component{}, spec.Services...), spec.Jobs...) {
		if c.Name == GateComponent {
			continue
		}
		for _, e := range c.Envs {
			if e.Value == secret || e.Key == deploy.GateEnvSecret {
				t.Fatalf("the gate secret reached %s", c.Name)
			}
		}
	}
	if we["APPTOWN_ORIGINS"].Value != appURL || we["APP_KEY"].Value != "kept" {
		t.Fatalf("the app's origin or its own secrets changed: %+v", we)
	}
	if strings.Contains(art.Hash, secret) {
		t.Fatal("the receipt's hash input carries the gate secret")
	}
}

// A gated placement the renderer cannot put whole behind the gate is
// refused before anything is made: an app the gate would expose, or one
// nobody could check, is never rendered.
func TestGatedRenderRefusesWhatItCannotGate(t *testing.T) {
	t.Parallel()
	d, copied := gatedDriver(t, nil)
	secret := strings.Repeat("ab", 32)
	for name, g := range map[string]*deploy.Gate{
		"no secret":     {Image: gateRef, Forge: "https://forge.example", Marker: testMarker},
		"not by digest": {Image: "registry.example/forge/gate:latest", Secret: secret, Forge: "https://forge.example", Marker: testMarker},
		"no AppTown":    {Image: gateRef, Secret: secret, Marker: testMarker},
		"no mark":       {Image: gateRef, Secret: secret, Forge: "https://forge.example"},
	} {
		if _, err := renderGated(t, d, g, scopedRenderSecrets{}); err == nil || !deploy.IsTerminal(err) {
			t.Fatalf("%s: rendered (%v)", name, err)
		}
	}
	if len(*copied) != 0 {
		t.Fatalf("an image was copied for a refused render: %+v", *copied)
	}
}

// The gate image is read with the credential AppTown gives for it, and
// a release's images keep their own: neither is sent where it was not
// given.
func TestGateImageIsReadWithItsOwnCredential(t *testing.T) {
	t.Parallel()
	given := oci.Credential{Username: "forge", Password: "gate-reader"}
	d, copied := gatedDriver(t, func(ref string) oci.Credential {
		if ref == gateRef {
			return given
		}
		return oci.Credential{}
	})
	if _, err := renderGated(t, d, &deploy.Gate{Image: gateRef, Secret: strings.Repeat("ab", 32), Forge: "https://forge.example", Marker: testMarker}, scopedRenderSecrets{}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, src := range *copied {
		if src.Reference == gateRef {
			found = src.Credential == given
		}
	}
	if !found {
		t.Fatalf("the gate image was not read with its credential: %+v", *copied)
	}
}
