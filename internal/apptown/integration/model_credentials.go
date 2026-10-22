package integration

import (
	"context"
	"strings"
)

// CheckModelCredentials is a model binding's live reading when it names the
// secrets it needs: each resolves, or the missing ones are named.
func (s *Service) CheckModelCredentials(ctx context.Context, secretEnv []string) Health {
	missing := []string{}
	all := s.secrets.All(ctx)
	for _, k := range secretEnv {
		if all[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return Health{OK: false, Detail: "secret not resolved: " + strings.Join(missing, ", ") + " (add to Infisical)"}
	}
	return Health{OK: true, Detail: "credentials resolved (" + strings.Join(secretEnv, ", ") + ")"}
}

// TestModelBinding is a model binding's Test connection with a draft
// configuration: the secrets it names (secretEnv, apiKeyFrom) resolve.
func (s *Service) TestModelBinding(ctx context.Context, config map[string]any) Health {
	var missing []string
	all := s.secrets.All(ctx)
	refs := append(ConfigStrings(config["secretEnv"]), ConfigStrings(config["apiKeyFrom"])...)
	for _, v := range refs {
		if all[v] == "" {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return Health{OK: false, Detail: "secret not resolved: " + strings.Join(missing, ", ")}
	}
	return Health{OK: true, Detail: "credentials resolved"}
}
