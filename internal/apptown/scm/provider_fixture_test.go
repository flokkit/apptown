package scm

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/gitlab/gitlabtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The driver's owner port is faked here; owner scope verification has its own
// fake HTTP tests. Git still goes through the real hardened HTTP transport.
func fixtureProviderIdentity(t *testing.T, fake *gitlabtest.Server, projectID ids.ID, remote string) (*RepositoryIdentity, func(context.Context, RepositoryIdentity) (VerifiedRepository, error)) {
	t.Helper()
	clone := fake.URL + "/fixture.git"
	if remote != "" {
		clone = fake.GitRepository(t, remote)
	}
	identity := RepositoryIdentity{AttachmentID: idstest.N(7801), ProjectID: projectID, Scope: ProviderScope{ConnectionID: idstest.N(7802), Kind: "gitlab", Base: fake.URL, AccountID: 41}, ProviderProjectID: 71, CloneURL: clone, Path: strings.TrimSuffix(strings.TrimPrefix(clone, fake.URL+"/"), ".git")}
	return &identity, func(_ context.Context, in RepositoryIdentity) (VerifiedRepository, error) {
		if in != identity {
			t.Fatal("driver replaced recorded repository identity")
		}
		client, err := fake.Client()
		return VerifiedRepository{Identity: identity, Provider: ProviderSession{Scope: identity.Scope, Client: client}, Observed: gitlab.ProjectRef{ID: 71, HTTPURL: clone, PathNS: identity.Path}}, err
	}
}
