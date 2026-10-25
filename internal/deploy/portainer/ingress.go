package portainer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"gopkg.in/yaml.v3"
)

// ReadStackIngress observes only the routing grammar emitted by IntranetStack.
// The caller supplies its verified scope/credential snapshot and current stack
// identity. Ownership and ingress are checked from one response; no provider
// write, creation entitlement or restored-copy activation is implied.
func ReadStackIngress(ctx context.Context, cfg Target, stack Stack, deployment ids.ID, legacy *StackAssociation) (deploy.Ingress, error) {
	if cfg.Scope == nil || cfg.Scope.Version != 1 || cfg.Scope.EndpointID != stack.EndpointID || stack.ID <= 0 || stack.Name == "" || deployment.IsZero() {
		return deploy.Ingress{}, errors.New("Portainer ingress has no verified stack scope")
	}
	content, err := StackFile(ctx, &cfg, strconv.FormatInt(stack.ID, 10))
	if err != nil {
		return deploy.Ingress{}, err
	}
	if err := verifyStackCompose(cfg, stack, deployment, legacy, content); err != nil {
		return deploy.Ingress{}, err
	}
	return stackIngress(content, stack.Name)
}

func stackIngress(content, router string) (deploy.Ingress, error) {
	var out deploy.Ingress
	var file struct {
		Services map[string]struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &file); err != nil {
		return out, fmt.Errorf("reading stack ingress: %w", err)
	}
	app, ok := file.Services["app"]
	if !ok {
		return out, errors.New("stack has no known public app service")
	}
	prefix := "traefik.http.routers." + router + "."
	labels := app.Labels
	if labels["traefik.enable"] != "true" || labels[prefix+"tls"] != "true" || labels[prefix+"tls.certresolver"] != "le" {
		return out, errors.New("stack uses unsupported public ingress settings")
	}
	rule := labels[prefix+"rule"]
	terms := strings.Split(rule, " || ")
	if len(terms) < 1 || len(terms) > 2 {
		return out, errors.New("stack has an ambiguous public host rule")
	}
	seen := map[string]bool{}
	for _, term := range terms {
		if !strings.HasPrefix(term, "Host(`") || !strings.HasSuffix(term, "`)") {
			return out, errors.New("stack uses an unsupported public host expression")
		}
		host := strings.TrimSuffix(strings.TrimPrefix(term, "Host(`"), "`)")
		normalized, err := deploy.NormalizeHostname(host)
		if err != nil || host != strings.TrimSpace(host) || seen[normalized] {
			return out, errors.New("stack has invalid or repeated public hostnames")
		}
		seen[normalized] = true
		out.Hosts = append(out.Hosts, host)
	}
	certificate := labels[prefix+"tls.domains[0].main"]
	if !strings.HasPrefix(certificate, "*.") {
		return out, errors.New("stack has no recognized certificate domain")
	}
	out.CertificateDomain = strings.TrimPrefix(certificate, "*.")
	if out.CertificateDomain != strings.TrimSpace(out.CertificateDomain) {
		return out, errors.New("stack certificate domain contains whitespace")
	}
	domain, err := deploy.NormalizeHostname(out.CertificateDomain)
	if err != nil {
		return out, err
	}
	generated, _ := deploy.NormalizeHostname(out.Hosts[0])
	if !strings.HasSuffix(generated, "."+domain) {
		return out, errors.New("stack generated host is outside its certificate domain")
	}
	allowed := map[string]bool{"traefik.enable": true, prefix + "rule": true, prefix + "tls": true, prefix + "tls.certresolver": true, prefix + "tls.domains[0].main": true, "traefik.http.services." + router + ".loadbalancer.server.port": true}
	for i, host := range out.Hosts[1:] {
		key := fmt.Sprintf("%stls.domains[%d].main", prefix, i+1)
		if labels[key] != host {
			return out, errors.New("stack custom host and certificate input disagree")
		}
		allowed[key] = true
	}
	for service, entry := range file.Services {
		for key := range entry.Labels {
			if strings.HasPrefix(key, "traefik.") && (service != "app" || !allowed[key]) {
				return out, errors.New("stack has additional unsupported ingress configuration")
			}
		}
	}
	port, err := strconv.Atoi(labels["traefik.http.services."+router+".loadbalancer.server.port"])
	if err != nil || port < 1 || port > 65535 {
		return out, errors.New("stack public service port is invalid")
	}
	out.URL = "https://" + out.Hosts[len(out.Hosts)-1]
	return out, nil
}
