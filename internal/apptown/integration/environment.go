package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"gopkg.in/yaml.v3"
)

var slugFieldRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// parseJSONMap reads a binding's config or resource, stored as a JSONB
// object. One that is not an object reads as empty, which every consumer
// treats as "not configured" (no registry, no connector settings): a refusal
// or a skipped integration, never a wrong value.
func parseJSONMap(raw string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

type Field struct {
	Key         string `yaml:"key" json:"key"`
	Label       string `yaml:"label" json:"label"`
	Type        string `yaml:"type,omitempty" json:"type,omitempty"` // string (default) | secretRef
	Help        string `yaml:"help,omitempty" json:"help,omitempty"`
	Placeholder string `yaml:"placeholder,omitempty" json:"placeholder,omitempty"`
	Optional    bool   `yaml:"optional,omitempty" json:"optional,omitempty"`
}

type EnvironmentEntry struct {
	Key   string `yaml:"key" json:"key"`
	Value string `yaml:"value" json:"value"`
	// Browser marks a value that is safe for anyone to read, so a static app's
	// page may be given it (/_apptown/env.js): a public key or endpoint, never a
	// credential. A value drawn from a secret is refused this mark.
	Browser bool `yaml:"browser,omitempty" json:"browser,omitempty"`
}

type Definition struct {
	Capability  string `yaml:"capability" json:"capability"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Icon        string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Connector   struct {
		Fields []Field `yaml:"fields,omitempty" json:"fields,omitempty"`
	} `yaml:"connector,omitempty" json:"connector"`
	Resource struct {
		Fields []Field `yaml:"fields,omitempty" json:"fields,omitempty"`
	} `yaml:"resource,omitempty" json:"resource"`
	Env      []EnvironmentEntry `yaml:"env,omitempty" json:"env,omitempty"`
	Steering string             `yaml:"steering,omitempty" json:"steering,omitempty"`
	Guidance string             `yaml:"guidance,omitempty" json:"guidance,omitempty"`
}

// reservedCapabilities are compiled-in: a service kind may not shadow them.
var reservedCapabilities = map[string]bool{
	CapSCM: true, CapSecrets: true, CapExecutor: true, CapModel: true,
	CapDeploy: true, "scanner": true, CapRegistry: true, "errors": true, "llm": true,
}

func ParseDefinition(yamlSrc string) (*Definition, error) {
	var k Definition
	if err := yaml.Unmarshal([]byte(yamlSrc), &k); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if !slugRe.MatchString(k.Capability) {
		return nil, fmt.Errorf("capability must be a slug, got %q", k.Capability)
	}
	if reservedCapabilities[k.Capability] {
		return nil, fmt.Errorf("capability %q is built in", k.Capability)
	}
	if k.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	for _, f := range append(append([]Field{}, k.Connector.Fields...), k.Resource.Fields...) {
		if !slugFieldRe.MatchString(f.Key) {
			return nil, fmt.Errorf("field key %q: use letters, digits, _ or -", f.Key)
		}
		if f.Type != "" && f.Type != "string" && f.Type != "secretRef" {
			return nil, fmt.Errorf("field %q: unknown type %q", f.Key, f.Type)
		}
	}
	for _, e := range k.Env {
		if e.Key == "" || strings.ContainsAny(e.Key, " =") {
			return nil, fmt.Errorf("env key %q is not a valid variable name", e.Key)
		}
		// Every visitor reads what a page is given; a secret must never be
		// one of those values, whoever writes the definition.
		if e.Browser && strings.Contains(e.Value, "{{secret:") {
			return nil, fmt.Errorf("env %s comes from a secret, so it cannot be given to browsers; remove browser: true", e.Key)
		}
	}
	return &k, nil
}

func (s *Service) Definitions(ctx context.Context) []*Definition {
	rows, err := s.ServiceKinds.All(ctx)
	if err != nil {
		log.Printf("reading integration service kinds: %v", err)
		return nil
	}
	var out []*Definition
	for _, k := range rows {
		if kind, err := ParseDefinition(k.YAML); err == nil {
			out = append(out, kind)
		}
	}
	return out
}

func (s *Service) Definition(ctx context.Context, capability string) *Definition {
	y, err := s.ServiceKinds.ByCapability(ctx, capability)
	if err != nil {
		return nil
	}
	k, err := ParseDefinition(y)
	if err != nil {
		return nil
	}
	return k
}

// RenderTemplate fills {{connector.K}}, {{resource.K}},
// {{project.slug}}/{{project.name}}, and {{secret:resource.K}}
// (resolveSecrets=true injects the secret VALUE — runtime wiring only;
// steering renders the reference name instead). Unresolved placeholders remain
// in the result; callers decide whether to omit or reject them.
func (s *Service) RenderTemplate(ctx context.Context, tmpl string, connectorCfg, resource map[string]any, project *project.Project, resolveSecrets bool) string {
	var values map[string]string
	if resolveSecrets && strings.Contains(tmpl, "{{secret:resource.") {
		values = s.secrets.All(ctx)
	}
	return renderTemplate(tmpl, connectorCfg, resource, project, resolveSecrets, values)
}

func renderTemplate(tmpl string, connectorCfg, resource map[string]any, project *project.Project, resolveSecrets bool, values map[string]string) string {
	out := tmpl
	if project != nil {
		out = strings.ReplaceAll(out, "{{project.slug}}", project.Slug)
		out = strings.ReplaceAll(out, "{{project.name}}", project.Name)
	}
	str := func(m map[string]any, k string) string {
		if m == nil {
			return ""
		}
		if s, ok := m[k].(string); ok {
			return s
		}
		if v, ok := m[k]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
	for k := range resource {
		name := str(resource, k)
		if resolveSecrets {
			if v := values[name]; v != "" {
				out = strings.ReplaceAll(out, "{{secret:resource."+k+"}}", v)
			}
		} else {
			out = strings.ReplaceAll(out, "{{secret:resource."+k+"}}", name+" (secret, injected at runtime)")
		}
		out = strings.ReplaceAll(out, "{{resource."+k+"}}", name)
	}
	for k := range connectorCfg {
		out = strings.ReplaceAll(out, "{{connector."+k+"}}", str(connectorCfg, k))
	}
	return out
}

// ConnectorConfig loads a named connector's config, verifying capability.
func (s *Service) ConnectorConfig(ctx context.Context, name, capability string) map[string]any {
	if name == "" {
		return nil
	}
	b, err := Selected(ctx, s.Bindings.db, capability, name)
	if err != nil {
		return nil
	}
	return parseJSONMap(b.ConfigJSON)
}

// attachedServiceIntegrations returns a project's service-kind integrations
// with their kind, connector config and resource — the render inputs.
type AttachedService struct {
	Kind      *Definition
	Connector string
	Config    map[string]any
	Resource  map[string]any
}

func (s *Service) AttachedServices(ctx context.Context, projectID ids.ID) []AttachedService {
	rows, err := AttachedRows(ctx, s.Bindings.db, projectID)
	if err != nil {
		return nil
	}
	var out []AttachedService
	for _, row := range rows {
		kind, err := ParseDefinition(row.KindYAML)
		if err != nil {
			continue
		}
		out = append(out, AttachedService{Kind: kind, Connector: row.Binding, Config: parseJSONMap(row.ConfigJSON), Resource: parseJSONMap(row.ResourceJSON)})
	}
	return out
}

// attachedServicesStrict is AttachedServices for a caller that must not read a
// failure as absence: an unreadable attachment, or a definition that no longer
// passes the rules (one saved before a value from a secret could no longer be
// marked for browsers), is an error that names it.
func (s *Service) attachedServicesStrict(ctx context.Context, projectID ids.ID) ([]AttachedService, error) {
	rows, err := AttachedRows(ctx, s.Bindings.db, projectID)
	if err != nil {
		return nil, err
	}
	var out []AttachedService
	for _, row := range rows {
		if strings.TrimSpace(row.KindYAML) == "" {
			continue // a built-in capability, not a service kind
		}
		kind, err := ParseDefinition(row.KindYAML)
		if err != nil {
			return nil, fmt.Errorf("the attached %s definition is no longer valid: %w", row.Capability, err)
		}
		config, err := strictJSONMap(row.ConfigJSON)
		if err != nil {
			return nil, fmt.Errorf("the attached %s configuration: %w", row.Capability, err)
		}
		resource, err := strictJSONMap(row.ResourceJSON)
		if err != nil {
			return nil, fmt.Errorf("the attached %s resource: %w", row.Capability, err)
		}
		out = append(out, AttachedService{Kind: kind, Connector: row.Binding, Config: config, Resource: resource})
	}
	return out, nil
}

func strictJSONMap(raw string) (map[string]any, error) {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" || raw == "null" {
		return out, nil
	}
	return out, json.Unmarshal([]byte(raw), &out)
}

// BrowserEnvironment is what a static app's page may be given: only values
// safe for any visitor to read — the error-reporting DSN and feedback key,
// which are public by design, and service-kind values marked browser: true.
// Secrets are never read here. A read failure is returned, not an empty set.
func (s *Service) BrowserEnvironment(ctx context.Context, projectID ids.ID) (map[string]string, error) {
	out := map[string]string{}
	if dsn, err := s.errorsDSN(ctx, projectID); err != nil {
		return nil, err
	} else if dsn != "" {
		public, err := publicDSN(dsn)
		if err != nil {
			return nil, err
		}
		inject, err := s.errorsInject(ctx)
		if err != nil && !errors.Is(err, errNoErrorsDefinition) {
			return nil, err
		} else if err != nil {
			log.Printf("error reporting for project %d: %v; its page is given no error-reporting values", projectID, err)
		}
		// Only the entries that carry the DSN, as its public form.
		for name, tmpl := range inject {
			if strings.Contains(tmpl, dsnRef) {
				out[name] = strings.ReplaceAll(tmpl, dsnRef, public)
			}
		}
	}
	if url, key, err := s.feedbackStrict(ctx, projectID); err != nil {
		return nil, err
	} else if url != "" && key != "" {
		out["FEEDBACK_URL"], out["FEEDBACK_APP_KEY"] = url, key
	}
	record, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	services, err := s.attachedServicesStrict(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, service := range services {
		for _, e := range service.Kind.Env {
			if !e.Browser || strings.Contains(e.Value, "{{secret:") {
				continue
			}
			v := renderTemplate(e.Value, service.Config, service.Resource, &record, true, nil)
			if strings.Contains(v, "{{") {
				continue // unresolved template: nothing true to give
			}
			out[e.Key] = v
		}
	}
	return out, nil
}

// Environment returns error/feedback contributions and rendered service-kind
// variables for application containers. It resolves named secrets to plaintext.
// Unresolved templates are omitted; attachment/definition read failures can also
// omit contributions. Callers must not use this result as a readiness check.
func (s *Service) Environment(ctx context.Context, projectID ids.ID) []string {
	env := s.ErrorsEnvironment(ctx, projectID)
	env = append(env, s.FeedbackEnvironment(ctx, projectID)...)
	var p *project.Project
	if record, err := s.projects.ByID(ctx, projectID); err == nil {
		p = &record
	}
	services := s.AttachedServices(ctx, projectID)
	var values map[string]string
	needsValues := false
	for _, service := range services {
		for _, entry := range service.Kind.Env {
			if strings.Contains(entry.Value, "{{secret:resource.") {
				needsValues = true
			}
		}
	}
	if needsValues {
		values = s.secrets.All(ctx)
	}
	for _, svc := range services {
		for _, e := range svc.Kind.Env {
			v := renderTemplate(e.Value, svc.Config, svc.Resource, p, true, values)
			if strings.Contains(v, "{{") {
				// The contract (design/subsystems/integration-runtime.md): an
				// unresolved placeholder is never given; the log names it. A
				// resolved value, blank included, is given as rendered.
				log.Printf("project %d: %s is not given %s: its value cannot be produced yet", projectID, svc.Connector, e.Key)
				continue
			}
			env = append(env, e.Key+"="+v)
		}
	}
	return env
}
