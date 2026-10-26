package projectlifecycle

import "github.com/flokkit/apptown/internal/ids"

// CreationSetup records the choices which follow local project creation.
// It identifies the submitted intent; it does not attest that its effects ran.
type CreationSetup struct {
	Origin           string            `json:"origin"` // empty, url or bundle
	SourceURL        string            `json:"sourceURL"`
	SourceBranch     string            `json:"sourceBranch"`
	CredentialSecret string            `json:"credentialSecret"` // a name, never a value
	BundleSHA256     string            `json:"bundleSHA256"`
	Services         []CreationService `json:"services"`
	Upstream         CreationUpstream  `json:"upstream"`
}

// CreationService is a selected catalog capability and its declared parameters.
type CreationService struct {
	Capability string            `json:"capability"`
	Params     map[string]string `json:"params"`
}

// CreationUpstream is independent of the local project address and request.
type CreationUpstream struct {
	Enabled             bool   `json:"enabled"`
	NamespaceID         int64  `json:"namespaceId"`
	RepositoryName      string `json:"repositoryName"`
	AttachmentRequestID ids.ID `json:"attachmentRequestId"`
}
