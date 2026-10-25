package portainer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func observedIngressCompose() string {
	return deploy.IntranetCompose(deploy.IntranetStack{Secrets: noSecrets{}, Prefix: "apptown-", Base: "fixture", Hosts: []string{"legacy.apps.example", "customer.example"}, Domain: "apps.example", Image: "example.invalid/app@sha256:123", Port: 8080})
}

func TestStackIngressPreservesKnownRendererRoutingAndRefusesAmbiguity(t *testing.T) {
	original := observedIngressCompose()
	want := deploy.Ingress{Hosts: []string{"legacy.apps.example", "customer.example"}, URL: "https://customer.example", CertificateDomain: "apps.example"}
	got, err := stackIngress(original, "apptown-fixture")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("observed routing: %+v %v", got, err)
	}
	for name, body := range map[string]string{
		"foreign router":                  strings.ReplaceAll(original, "routers.apptown-fixture.", "routers.another."),
		"unknown host expression":         strings.Replace(original, "Host(`legacy.apps.example`)", "HostRegexp(`.*`)", 1),
		"duplicate host":                  strings.ReplaceAll(original, "customer.example", "legacy.apps.example"),
		"unmatched certificate":           strings.Replace(original, "*.apps.example", "*.elsewhere.example", 1),
		"extra router":                    strings.Replace(original, "    labels:\n", "    labels:\n      traefik.http.routers.second.rule: 'Host(`other.example`)'\n", 1),
		"extra TLS setting":               strings.Replace(original, "    labels:\n", "    labels:\n      traefik.http.routers.apptown-fixture.tls.options: unsafe\n", 1),
		"duplicate mapping":               strings.Replace(original, "    labels:\n", "    labels:\n      traefik.enable: false\n", 1),
		"unknown resolver":                strings.Replace(original, "certresolver: \"le\"", "certresolver: \"other\"", 1),
		"URL host":                        strings.ReplaceAll(original, "customer.example", "https://customer.example"),
		"certificate trailing whitespace": strings.Replace(original, "*.apps.example", "*.apps.example ", 1),
		"certificate leading whitespace":  strings.Replace(original, "*.apps.example", "*. apps.example", 1),
		"host whitespace":                 strings.ReplaceAll(original, "customer.example", " customer.example "),
		"missing service":                 "services: {}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := stackIngress(body, "apptown-fixture"); err == nil {
				t.Fatal("unsupported routing accepted")
			}
		})
	}
}

func TestStackIngressUsesTheSameResponseAsOwnershipProof(t *testing.T) {
	id := idstest.N(10)
	content, err := MarkCompose(observedIngressCompose(), id)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/stacks/4/file" {
			t.Errorf("unexpected provider effect %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"StackFileContent": content})
	}))
	defer server.Close()
	scope := Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "engine"}
	cfg := Target{PortainerURL: server.URL, Scope: &scope}
	stack := Stack{ID: 4, Name: "apptown-fixture", EndpointID: 3}
	got, err := ReadStackIngress(t.Context(), cfg, stack, id, nil)
	if err != nil || got.URL != "https://customer.example" || calls != 1 {
		t.Fatalf("observation %+v, calls%d: %v", got, calls, err)
	}
	content = strings.Replace(content, id.String(), idstest.N(11).String(), 1)
	if _, err := ReadStackIngress(t.Context(), cfg, stack, id, nil); err == nil {
		t.Fatal("routing from another deployment was accepted")
	}
	before := calls
	invalid := scope
	invalid.Version = 0
	cfg.Scope = &invalid
	if _, err := ReadStackIngress(t.Context(), cfg, stack, id, nil); err == nil || calls != before {
		t.Fatal("invalid identity reached provider")
	}
}
