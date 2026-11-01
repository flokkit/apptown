package scm

import (
	"context"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
)

// AttachmentState reports durable knowledge without making network calls or
// adopting an unverified origin. A pending result is never represented as empty.
type AttachmentState struct {
	State             string             `json:"state"`
	Remote            string             `json:"remote"`
	RepositoryName    string             `json:"repositoryName"`
	AttachmentID      ids.ID             `json:"attachmentId"`
	ProviderProjectID int64              `json:"providerProjectId"`
	Request           *AttachmentRequest `json:"request,omitempty"`
	CanResume         bool               `json:"canResume"`
}

func (s *RepositoryAttachments) State(ctx context.Context, projectID ids.ID) (AttachmentState, error) {
	caller, err := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectIntegrations, Resource: access.Resource{Type: "project", ProjectID: projectID}})
	if err != nil {
		return AttachmentState{}, err
	}
	release, err := s.locks.Acquire(ctx, projectID)
	if err != nil {
		return AttachmentState{}, err
	}
	defer release()
	attachment, found, err := readAttachment(ctx, s.db, projectID)
	if err != nil {
		return AttachmentState{}, err
	}
	if found {
		return AttachmentState{State: "attached", Remote: attachment.CloneURL, AttachmentID: attachment.AttachmentID, ProviderProjectID: attachment.ProviderProjectID}, nil
	}
	intent, found, err := s.readIntent(ctx, projectID)
	if err != nil {
		return AttachmentState{}, err
	}
	if found {
		request := AttachmentRequest{RequestID: intent.row.RequestID, Create: intent.parameters.Create, URL: intent.parameters.URL, NamespaceID: intent.parameters.Namespace, RepositoryName: intent.parameters.Name}
		return AttachmentState{State: intent.row.Phase, Remote: intent.result.HTTPURL, RepositoryName: intent.parameters.Name, AttachmentID: intent.row.ID, ProviderProjectID: intent.result.ID, Request: &request, CanResume: caller.Principal.ID == intent.row.ActorID}, nil
	}
	p, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return AttachmentState{}, err
	}
	if p.DefaultBranch == "" {
		return AttachmentState{State: "none"}, nil
	}
	repo, err := p.Repository(s.dataDir)
	if err != nil {
		return AttachmentState{}, err
	}
	origin, err := readOrigin(ctx, repo)
	if err != nil {
		return AttachmentState{}, err
	}
	if origin != "" {
		return AttachmentState{State: "unverified", Remote: PublicRemoteURL(origin)}, nil
	}
	return AttachmentState{State: "none"}, nil
}
