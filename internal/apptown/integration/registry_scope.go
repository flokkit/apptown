package integration

import (
	"strings"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/google/go-containerregistry/pkg/name"
)

// registryDestination binds the login host to the destination OCI actually
// selects. A connector may rotate credentials there, but cannot redirect an
// attachment's token or lend its credential to a different retained repository.
func registryDestination(repository, connectorHost string) (string, error) {
	repo, err := name.NewRepository(repository)
	if err != nil {
		return "", fault.New(fault.Unprocessable, "the container-registry attachment needs a valid repository, without a tag or digest")
	}
	authority, err := name.NewRegistry(strings.ToLower(repo.RegistryStr()), name.StrictValidation)
	if err != nil {
		return "", fault.New(fault.Unprocessable, "the container-registry repository needs a valid registry host")
	}
	host := authority.RegistryStr()
	if connectorHost != "" {
		connector, err := name.NewRegistry(strings.ToLower(strings.TrimSuffix(connectorHost, "/")), name.StrictValidation)
		if err != nil {
			return "", fault.New(fault.Unprocessable, "the container-registry connector needs a registry host, optionally with a port, without a scheme or path")
		}
		if !strings.EqualFold(connector.RegistryStr(), host) {
			return "", fault.New(fault.Unprocessable, "the container-registry connector and attached repository name different registries; restore the connector host or replace the attachment")
		}
	}
	return host, nil
}
