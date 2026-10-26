package portainer

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"net/url"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"gopkg.in/yaml.v3"
)

const readinessKey = "x-apptown-readiness"

// Readiness is the admitted, non-secret expectation for one rendered release.
// It is part of the artifact bytes; execution adds generation labels separately.
type Readiness struct {
	URL        string      `json:"url" yaml:"url"`
	Version    int         `json:"version" yaml:"version"`
	Deployment ids.ID      `json:"deployment" yaml:"deployment"`
	Target     ids.ID      `json:"target" yaml:"target"`
	Revision   string      `json:"revision" yaml:"revision"`
	Components []Component `json:"components" yaml:"components"`
	Public     Probe       `json:"public" yaml:"public"`
}

// Component identifies an application-owned service, never a shared need.
type Component struct {
	Service     string `json:"service" yaml:"service"`
	Image       string `json:"image" yaml:"image"`
	Init        bool   `json:"init,omitempty" yaml:"init,omitempty"`
	ShellHealth bool   `json:"shellHealth,omitempty" yaml:"shellHealth,omitempty"`
}

// Probe is the supported HTTP readiness intent for the public container.
// StaticRelease, when present, requires a second exact release-identity answer.
type Probe struct {
	ToolImage     string                    `json:"toolImage" yaml:"toolImage"`
	Port          int                       `json:"port" yaml:"port"`
	Path          string                    `json:"path" yaml:"path"`
	Headers       map[string]string         `json:"headers,omitempty" yaml:"headers,omitempty"`
	Expect        int                       `json:"expect,omitempty" yaml:"expect,omitempty"`
	GraceNanos    int64                     `json:"graceNanos,omitempty" yaml:"graceNanos,omitempty"`
	StaticRelease string                    `json:"staticRelease,omitempty" yaml:"staticRelease,omitempty"`
	Policy        *appcontract.HealthPolicy `json:"policy,omitempty" yaml:"policy,omitempty"`
}

func (r Readiness) validate() error {
	if (r.Version != 1 && r.Version != 2) || r.Deployment.IsZero() || r.Target.IsZero() || strings.TrimSpace(r.Revision) == "" || len(r.Components) == 0 {
		return errors.New("Portainer readiness has incomplete release identity")
	}
	if r.Version == 1 && r.Public.Policy != nil {
		return errors.New("legacy Portainer readiness cannot carry policy")
	}
	if r.Version == 2 {
		if r.Public.Policy == nil {
			return errors.New("Portainer readiness policy is missing")
		}
		if err := validatePolicy(r.Public.Policy); err != nil {
			return err
		}
		if int64(r.Public.Policy.StartupGrace) != r.Public.GraceNanos {
			return errors.New("Portainer readiness grace differs from its policy")
		}
	}
	origin, err := url.Parse(r.URL)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.Fragment != "" {
		return errors.New("Portainer readiness has no admitted public URL")
	}
	if !immutableImage(r.Public.ToolImage) {
		return errors.New("Portainer readiness requires a pinned diagnostic image")
	}
	if r.Public.Port < 1 || r.Public.Port > 65535 || r.Public.GraceNanos < 0 || (r.Public.Expect != 0 && (r.Public.Expect < 100 || r.Public.Expect > 599)) {
		return errors.New("Portainer readiness has invalid HTTP probe settings")
	}
	u, err := url.ParseRequestURI(r.Public.Path)
	if err != nil || !strings.HasPrefix(r.Public.Path, "/") || strings.HasPrefix(r.Public.Path, "//") || u.IsAbs() || u.Host != "" || u.Fragment != "" {
		return errors.New("Portainer readiness requires a local HTTP path")
	}
	seen := map[string]bool{}
	public := false
	for _, c := range r.Components {
		if !immutableImage(c.Image) || c.Service == "" || seen[c.Service] || (c.Init && c.ShellHealth) {
			return errors.New("Portainer readiness has ambiguous components or nonimmutable images")
		}
		seen[c.Service] = true
		if c.Service == "app" {
			if c.Init {
				return errors.New("Portainer public component cannot be an init step")
			}
			public = true
		}
	}
	if !public {
		return errors.New("Portainer readiness has no public component")
	}
	return nil
}

// WithReadiness retains the exact release expectation in the canonical Compose
// artifact, after verifying that every expected service uses its admitted image.
func WithReadiness(compose string, r Readiness) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	marker, err := composeDeployment(compose)
	if err != nil || marker != r.Deployment.String() {
		return "", errors.New("Portainer readiness does not match its deployment marker")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return "", err
	}
	root := doc.Content[0]
	if mapValue(root, readinessKey) != nil {
		return "", errors.New("Portainer artifact already has readiness intent")
	}
	services := mapValue(root, "services")
	for _, c := range r.Components {
		service := mapValue(services, c.Service)
		image := mapValue(service, "image")
		if image == nil || image.Value != c.Image {
			return "", fmt.Errorf("Portainer readiness image differs for component %s", c.Service)
		}
		health := mapValue(service, "healthcheck")
		if c.ShellHealth && health == nil {
			return "", fmt.Errorf("Portainer readiness healthcheck missing for component %s", c.Service)
		}
	}
	var node yaml.Node
	if err := node.Encode(r); err != nil {
		return "", err
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: readinessKey}, &node)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func mapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func immutableImage(image string) bool {
	repository, digest, ok := strings.Cut(image, "@sha256:")
	return ok && repository != "" && len(digest) == 64 && strings.Trim(digest, "0123456789abcdef") == ""
}

// PublicHealthPolicy normalizes the public probe over Portainer's existing cold
// polling and diagnostic defaults. Omitted policy keeps those execution paths.
func PublicHealthPolicy(c *appcontract.Contract) (appcontract.HealthPolicy, error) {
	var probe *appcontract.Probe
	if c != nil && c.PublicPart() != nil && c.PublicPart().Intent != nil {
		probe = c.PublicPart().Intent.Readiness
	}
	policy, err := appcontract.NormalizeHealthPolicy(probe, "readiness", appcontract.HealthPolicy{Period: time.Second, Timeout: 8 * time.Second, FailureThreshold: 1, SuccessThreshold: 1})
	if err != nil {
		return policy, err
	}
	return policy, policy.CheckBootFit()
}

// RefusesHealth checks every executed application part before provider effects.
// Legacy worker shell checks and ordered init completion retain their owners.
func RefusesHealth(c *appcontract.Contract) error {
	if c == nil {
		return errors.New("Portainer health requires a contract")
	}
	for _, part := range c.Parts {
		if part.Intent == nil {
			continue
		}
		if part.Intent.Liveness != nil {
			return fmt.Errorf("part %s: Portainer does not support liveness intent", part.Name)
		}
		if part.Intent.Readiness != nil {
			probe := part.Intent.Readiness
			if !part.Public {
				return fmt.Errorf("part %s: Portainer does not support nonpublic structured readiness", part.Name)
			}
			if probe.HTTP == "" || probe.TCP != 0 || len(probe.Exec) > 0 {
				return fmt.Errorf("part %s: Portainer supports HTTP readiness here", part.Name)
			}
		}
	}
	_, err := PublicHealthPolicy(c)
	return err
}

func validatePolicy(p *appcontract.HealthPolicy) error {
	if p == nil || !p.Explicit || (p.Period != time.Second && (p.Period < 5*time.Second || p.Period > 300*time.Second)) || p.Period%time.Second != 0 || p.Timeout < time.Second || p.Timeout > 10*time.Second || p.Timeout%time.Second != 0 || p.StartupGrace < 0 || p.FailureThreshold < 1 || p.FailureThreshold > 50 || p.SuccessThreshold < 1 || p.SuccessThreshold > 50 {
		return errors.New("Portainer readiness policy is invalid")
	}
	return p.CheckBootFit()
}
