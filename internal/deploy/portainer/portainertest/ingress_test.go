package portainertest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

func observedIngressCompose() string {
	return deploy.IntranetCompose(deploy.IntranetStack{Secrets: noSecrets{}, Prefix: "apptown-", Base: "fixture", Hosts: []string{"legacy.apps.example", "customer.example"}, Domain: "apps.example", Image: "example.invalid/app@sha256:123", Port: 8080})
}

func TestStackIngressPreservesKnownRendererRoutingAndRefusesAmbiguity(t *testing.T) {
	original := observedIngressCompose()
	want := deploy.Ingress{Hosts: []string{"legacy.apps.example", "customer.example"}, URL: "https://customer.example", CertificateDomain: "apps.example"}
	got, err := StackIngress(original, "apptown-fixture")
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
			if _, err := StackIngress(body, "apptown-fixture"); err == nil {
				t.Fatal("unsupported routing accepted")
			}
		})
	}
}

// noSecrets delivers no secrets to any consumer.
type noSecrets struct{}

func (noSecrets) EnvFor(string) map[string]string { return nil }
