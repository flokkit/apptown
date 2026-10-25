package apptown

import (
	"context"
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"strconv"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/gitlab"
)

type repositoryProvider struct {
	bindings *integration.Bindings
	secrets  *secret.Service
}

func (p repositoryProvider) Client(ctx context.Context) (*gitlab.Client, error) {
	return configuredGitLab(ctx, p.secrets)
}

// Snapshot pins the connector and credential values before observing the account.
// A retained identity from another connector or host is refused before any token
// is sent; all later effects retain this same client.
func (p repositoryProvider) Snapshot(ctx context.Context, expected *scm.ProviderScope) (scm.ProviderSession, error) {
	if p.bindings == nil {
		return scm.ProviderSession{}, fault.New(fault.Unavailable, "repository connection is unavailable")
	}
	binding, err := p.bindings.ByName(ctx, "gitlab")
	if err != nil {
		return scm.ProviderSession{}, err
	}
	if binding.Provider != "gitlab" || binding.Capability != integration.CapSCM {
		return scm.ProviderSession{}, fault.New(fault.Conflict, "repository connection is not GitLab")
	}
	var config struct {
		URLFrom   string `json:"urlFrom"`
		TokenFrom string `json:"tokenFrom"`
	}
	if err := json.Unmarshal([]byte(binding.ConfigJSON), &config); err != nil {
		return scm.ProviderSession{}, err
	}
	if config.URLFrom == "" || config.TokenFrom == "" {
		return scm.ProviderSession{}, fault.New(fault.Conflict, "repository connection has no credential sources")
	}
	values, err := p.secrets.WithContext(ctx)
	if err != nil {
		return scm.ProviderSession{}, err
	}
	base, err := gitlab.CanonicalBase(values[config.URLFrom])
	if err != nil {
		return scm.ProviderSession{}, err
	}
	scope := scm.ProviderScope{ConnectionID: binding.ID, Kind: "gitlab", Base: base}
	if expected != nil && (expected.ConnectionID != scope.ConnectionID || expected.Kind != scope.Kind || expected.Base != scope.Base) {
		return scm.ProviderSession{}, fault.New(fault.Conflict, "repository connection changed; the recorded provider identity needs attention")
	}
	client, err := gitlab.New(base, values[config.TokenFrom])
	if err != nil {
		return scm.ProviderSession{}, err
	}
	scope.AccountID, err = client.AccountID(ctx)
	if err != nil {
		return scm.ProviderSession{}, err
	}
	if expected != nil && expected.AccountID != scope.AccountID {
		return scm.ProviderSession{}, fault.New(fault.Conflict, "repository credentials belong to a different provider account")
	}
	namespace := 0
	if raw := values["GITLAB_NAMESPACE_ID"]; raw != "" {
		namespace, err = strconv.Atoi(raw)
		if err != nil || namespace < 0 {
			return scm.ProviderSession{}, fault.New(fault.Invalid, "configured repository namespace is invalid")
		}
	}
	return scm.ProviderSession{Scope: scope, Client: client, DefaultNamespace: namespace}, nil
}

// scmRepositoryTokens adapts the repository owner's identity to integration's
// token resource without a domain dependency or a second credential lookup.
type scmRepositoryTokens struct{ repositories *scm.RepositoryAttachments }

func (p scmRepositoryTokens) Resolve(ctx context.Context, id ids.ID) (integration.SCMRepository, error) {
	verified, err := p.repositories.Resolve(ctx, id)
	if err != nil {
		return integration.SCMRepository{}, err
	}
	raw, err := json.Marshal(verified.Identity)
	if err != nil {
		return integration.SCMRepository{}, err
	}
	return integration.SCMRepository{IdentityJSON: string(raw), Project: verified.Observed, Client: verified.Provider.Client}, nil
}
