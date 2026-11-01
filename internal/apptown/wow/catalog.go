package wow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/version"
)

// The catalog: canned change definitions the release ships in
// stock/catalog.yaml. Like the other stock ways of working they are files,
// read from the release's own copy on every list, show and apply; nothing
// is stored. A run started from one records the entry's name and the
// release it came from, which together name the exact definition.

type CatalogParam struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Required    bool   `json:"required,omitempty" yaml:"required"`
	Default     string `json:"default,omitempty" yaml:"default"`
}

type CatalogDef struct {
	Params []CatalogParam `json:"params,omitempty" yaml:"params"`
	// Prompt template — {{name}} placeholders filled from params. Becomes
	// the change body its workflow's agent receives.
	Prompt   string `json:"prompt" yaml:"prompt"`
	Workflow string `json:"workflow" yaml:"workflow"`     // the workflow that runs it
	Title    string `json:"title,omitempty" yaml:"title"` // change title template; default "<name>"
	Type     string `json:"type,omitempty" yaml:"type"`   // change type; default "chore"
	// Model pin: wins over project slots.
	Model string `json:"model,omitempty" yaml:"model"`
	// Whitelisted workflow knobs — effort, never gates.
	Thinking       string `json:"thinking,omitempty" yaml:"thinking"`
	TimeoutMinutes int    `json:"timeoutMinutes,omitempty" yaml:"timeoutMinutes"`
}

// CatalogChange is one entry of stock/catalog.yaml.
type CatalogChange struct {
	Name        string     `json:"name" yaml:"name"`
	Description string     `json:"description" yaml:"description"`
	Definition  CatalogDef `json:"-" yaml:"definition"`
}

var (
	errCatalogName    = fault.New(fault.Invalid, "name must look like uplift/go-version")
	ErrCatalogMissing = errors.New("no catalog change named")
)

// readStockCatalog reads the catalog the release ships (catalog.yaml), each
// entry named in the catalog's shape and its definition valid. A file that
// does not read is an error, never an empty catalog.
func readStockCatalog(fsys fs.FS) ([]CatalogChange, error) {
	raw, err := fs.ReadFile(fsys, "catalog.yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Catalog []CatalogChange `yaml:"catalog"`
	}
	if err := decodeStrict(raw, &doc); err != nil {
		return nil, fmt.Errorf("catalog.yaml: %w", err)
	}
	seen := map[string]bool{}
	for i := range doc.Catalog {
		c := &doc.Catalog[i]
		if !validName(c.Name) {
			return nil, fmt.Errorf("catalog.yaml: %q: %w (%s)", c.Name, errCatalogName, nameShape)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("catalog.yaml: %q is listed twice", c.Name)
		}
		seen[c.Name] = true
		if err := c.Definition.validate(); err != nil {
			return nil, fmt.Errorf("catalog.yaml: %s: %w", c.Name, err)
		}
	}
	return doc.Catalog, nil
}

// validate is what a definition must have: the workflow that runs it and
// uniquely named params. An absent type is "chore".
func (d *CatalogDef) validate() error {
	if strings.TrimSpace(d.Workflow) == "" {
		return fmt.Errorf("definition needs a workflow (the one that runs it)")
	}
	seen := map[string]bool{}
	for _, p := range d.Params {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("every param needs a name")
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate param %q", p.Name)
		}
		seen[p.Name] = true
	}
	if d.Type == "" {
		d.Type = "chore"
	}
	return nil
}

// Render fills {{name}} placeholders in one of the definition's templates.
// Unknown params are an error (a typo must not silently reach the agent);
// missing required params are an error; optional params fall back to
// their defaults.
func (d *CatalogDef) Render(tmpl string, given map[string]string) (string, error) {
	known := map[string]bool{}
	out := tmpl
	for _, p := range d.Params {
		known[p.Name] = true
		v, ok := given[p.Name]
		if !ok || v == "" {
			if p.Required {
				return "", fmt.Errorf("param %q is required", p.Name)
			}
			v = p.Default
		}
		out = strings.ReplaceAll(out, "{{"+p.Name+"}}", v)
	}
	for k := range given {
		if !known[k] {
			return "", fmt.Errorf("unknown param %q", k)
		}
	}
	if i := strings.Index(out, "{{"); i >= 0 {
		if j := strings.Index(out[i:], "}}"); j >= 0 {
			return "", fmt.Errorf("template placeholder %s has no param", out[i:i+j+2])
		}
	}
	return out, nil
}

// CatalogRelease is the release whose catalog.yaml a run's entry came from.
func CatalogRelease() string { return version.String() }

// RunInputs is what a run started from the catalog carries: which entry,
// from which release's catalog, applied with which params, and the
// definition's effort knobs (model pin, thinking, timeout — never gates).
func (c *CatalogChange) RunInputs(params map[string]string) map[string]any {
	def := c.Definition
	knobs := map[string]any{
		"name":    c.Name,
		"release": CatalogRelease(),
		"applied": params,
	}
	if def.Model != "" {
		knobs["model"] = def.Model
	}
	if def.Thinking != "" {
		knobs["thinking"] = def.Thinking
	}
	if def.TimeoutMinutes > 0 {
		knobs["timeoutMinutes"] = def.TimeoutMinutes
	}
	return map[string]any{"catalog": knobs}
}

// CatalogKnobs reads the catalog's effort knobs back out of a run's
// inputs, nil when the run was not started from the catalog.
func CatalogKnobs(inputsJSON string) *CatalogDef {
	if inputsJSON == "" {
		return nil
	}
	var in struct {
		Catalog *struct {
			Model          string `json:"model"`
			Thinking       string `json:"thinking"`
			TimeoutMinutes int    `json:"timeoutMinutes"`
		} `json:"catalog"`
	}
	if json.Unmarshal([]byte(inputsJSON), &in) != nil || in.Catalog == nil {
		return nil
	}
	return &CatalogDef{Model: in.Catalog.Model, Thinking: in.Catalog.Thinking, TimeoutMinutes: in.Catalog.TimeoutMinutes}
}
