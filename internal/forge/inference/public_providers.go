package inference

import (
	"context"
	"fmt"
	"sort"
)

// PublicProvider is the credential boundary for generated agent configuration:
// a provider as a harness may see it. There is no field for an upstream
// credential, so a harness adapter cannot render one by mistake, and the view
// is checked against every private binding before it is released. Which
// harness consumes it, and in what file format, is that harness's business;
// this package names no harness.
type PublicProvider struct {
	Name, URL, API string
	Metadata       map[string]any
	Models         []map[string]any
}

// PublicProviders projects the catalog into the public view: allowed models
// only, proxy URLs under baseURL, and no credential anywhere in it. A binding
// whose metadata carries a credential refuses the whole projection rather than
// releasing a partial one.
func (catalog Catalog) PublicProviders(ctx context.Context, baseURL string) ([]PublicProvider, error) {
	ps, err := catalog.providers(ctx)
	if err != nil {
		return nil, err
	}
	public := make([]PublicProvider, 0, len(ps))
	for name, p := range ps {
		view := PublicProvider{Name: name, URL: baseURL + "/providers/" + name, API: p.api, Metadata: p.metadata}
		for id, m := range p.models {
			if p.allows(id) {
				view.Models = append(view.Models, m)
			}
		}
		public = append(public, view)
	}
	sort.Slice(public, func(i, j int) bool { return public[i].Name < public[j].Name })
	// Check the values themselves, before anything serializes them. A scan of
	// rendered JSON is not a credential check: encoding escapes < > & to \u003c
	// and friends, so a credential containing one of them survives a substring
	// search of the output (found in review, 17 September). Every string the
	// view can carry — names, URLs, metadata values and keys, model fields —
	// is inspected as the value a harness would eventually write.
	for _, p := range ps {
		for _, view := range public {
			if leaks := credentialIn(view, p); leaks {
				return nil, fmt.Errorf("model binding %q places a credential in public model metadata", p.name)
			}
		}
	}
	return public, nil
}

// credentialIn reports whether any string reachable from the view, including
// map keys, contains one of the provider's credentials.
func credentialIn(view PublicProvider, p *inferenceProvider) bool {
	if containsInferenceCredential(view.Name, p) || containsInferenceCredential(view.URL, p) || containsInferenceCredential(view.API, p) {
		return true
	}
	if credentialInValue(view.Metadata, p) {
		return true
	}
	for _, m := range view.Models {
		if credentialInValue(m, p) {
			return true
		}
	}
	return false
}

func credentialInValue(v any, p *inferenceProvider) bool {
	switch x := v.(type) {
	case string:
		return containsInferenceCredential(x, p)
	case map[string]any:
		for k, inner := range x {
			if containsInferenceCredential(k, p) || credentialInValue(inner, p) {
				return true
			}
		}
	case []any:
		for _, inner := range x {
			if credentialInValue(inner, p) {
				return true
			}
		}
	case []map[string]any:
		for _, inner := range x {
			if credentialInValue(inner, p) {
				return true
			}
		}
	case []string:
		for _, inner := range x {
			if containsInferenceCredential(inner, p) {
				return true
			}
		}
	}
	return false
}

// CheckEnvironment rejects aliases of known provider credentials on the runtime
// delivery surface. This policy belongs with private provider access, not with
// environment preparation or a particular harness.
func (catalog Catalog) CheckEnvironment(ctx context.Context, env []string) error {
	ps, err := catalog.providers(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		for _, kv := range env {
			if containsInferenceCredential(kv, p) {
				return fmt.Errorf("runtime environment contains a provider credential from binding %q; remove the overlapping secret or integration setting", p.name)
			}
		}
	}
	return nil
}
