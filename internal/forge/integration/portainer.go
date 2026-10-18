package integration

import (
	"context"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/deploy/portainer"
)

func (s *Service) PortainerTarget(ctx context.Context) (*portainer.Target, error) {
	// The portainer-deploy binding (integrations.go) is the visible face of this
	// config: it names WHICH secrets hold the credentials (references only,
	// per the integrations rule) and the Portainer environment. Resolution
	// happens here, at use time.
	binding := s.ConnectorConfig(ctx, "portainer-deploy", "deploy")
	ref := func(key, fallback string) string {
		if v, _ := binding[key].(string); v != "" {
			return v
		}
		return fallback
	}
	values := s.secrets.All(ctx)
	cfg := &portainer.Target{
		PortainerURL:      strings.TrimRight(values[ref("urlFrom", "PORTAINER_URL")], "/"),
		PortainerKey:      values[ref("apiKeyFrom", "PORTAINER_API_KEY")],
		AppsDomain:        values[ref("appsDomainFrom", "FORGE_APPS_DOMAIN")],
		EnvName:           ref("environment", ""),
		ReleaseDeployment: ref("releaseDeployment", "lan"),
	}
	if cfg.EnvName == "" { // a binding written without one; the seed always names it
		cfg.EnvName = DefaultPortainerEnvironment
	}
	var missing []string
	for k, v := range map[string]string{
		ref("urlFrom", "PORTAINER_URL"):            cfg.PortainerURL,
		ref("apiKeyFrom", "PORTAINER_API_KEY"):     cfg.PortainerKey,
		ref("appsDomainFrom", "FORGE_APPS_DOMAIN"): cfg.AppsDomain,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the intranet isn't wired up yet — these secret names don't resolve: %s (add them in Infisical or secrets.env)",
			strings.Join(missing, ", "))
	}
	return cfg, nil
}
