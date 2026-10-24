package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/deploy/portainer"
)

// ErrIntranetNotWired is an intranet whose secret names do not resolve: a
// configuration state (not set up yet), not a failed read.
var ErrIntranetNotWired = errors.New("the intranet isn't wired up yet")

// PortainerTarget is the intranet's connection. Names that do not resolve
// are ErrIntranetNotWired; a connector or secrets store that cannot be read
// is another error.
func (s *Service) PortainerTarget(ctx context.Context) (*portainer.Target, error) {
	// The portainer-deploy binding (integrations.go) is the visible face of this
	// config: it names WHICH secrets hold the credentials (references only,
	// per the integrations rule). Resolution happens here, at use time. The
	// Portainer environment is the target's own setting (PortainerConfig).
	binding, err := s.ConnectorConfig(ctx, "portainer-deploy", "deploy")
	if err != nil {
		return nil, err
	}
	ref := func(key, fallback string) string {
		if v, _ := binding[key].(string); v != "" {
			return v
		}
		return fallback
	}
	values, err := s.secrets.WithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the intranet's secrets: %w", err)
	}
	cfg := &portainer.Target{
		PortainerURL:      strings.TrimRight(values[ref("urlFrom", "PORTAINER_URL")], "/"),
		PortainerKey:      values[ref("apiKeyFrom", "PORTAINER_API_KEY")],
		AppsDomain:        values[ref("appsDomainFrom", "APPTOWN_APPS_DOMAIN")],
		ReleaseDeployment: ref("releaseDeployment", "lan"),
	}
	var missing []string
	for k, v := range map[string]string{
		ref("urlFrom", "PORTAINER_URL"):              cfg.PortainerURL,
		ref("apiKeyFrom", "PORTAINER_API_KEY"):       cfg.PortainerKey,
		ref("appsDomainFrom", "APPTOWN_APPS_DOMAIN"): cfg.AppsDomain,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w — these secret names don't resolve: %s (add them in Infisical or secrets.env)",
			ErrIntranetNotWired, strings.Join(missing, ", "))
	}
	return cfg, nil
}
