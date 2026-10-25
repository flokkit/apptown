package scm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm/internal/scmsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/ids"
)

// AttachmentRequest names one immutable upstream choice. RequestID is retained
// by the client across lost responses. Omission resumes the project's one
// existing intent; it never silently starts another create.
type AttachmentRequest struct {
	ProjectID            ids.ID `json:"-"`
	RequestID            ids.ID `json:"requestId"`
	ExpectedAttachmentID ids.ID `json:"expectedAttachmentId"`
	URL                  string `json:"url"`
	Create               bool   `json:"create"`
	NamespaceID          int    `json:"namespaceId"`
	RepositoryName       string `json:"repositoryName"`
	// Recovery only identifies a provider result bearing this intent's original
	// association and creator. A matching repository name alone proves nothing.
	RecoverProviderID int64 `json:"recoverProviderId"`
}

type attachmentParameters struct {
	Create    bool   `json:"create"`
	Name      string `json:"name"`
	Namespace int    `json:"namespace"`
	URL       string `json:"url"`
	Branch    string `json:"branch"`
}

type attachmentIntent struct {
	row        scmsql.RepositoryAttachmentIntent
	parameters attachmentParameters
	scope      ProviderScope
	result     gitlab.ProjectRef
}

func (i attachmentIntent) refusal() error {
	return fault.New(fault.Conflict, fmt.Sprintf("upstream attachment %s is %s; resume its original request or identify its verified provider result", i.row.ID, i.row.Phase))
}
func (i attachmentIntent) association() string {
	return "AppTown repository attachment " + i.row.ID.String()
}

func (s *RepositoryAttachments) readIntent(ctx context.Context, projectID ids.ID) (attachmentIntent, bool, error) {
	row, err := scmsql.New(s.db.SQL()).RepositoryAttachmentIntent(ctx, projectID)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return attachmentIntent{}, false, err
	}
	i := attachmentIntent{row: row, scope: ProviderScope{ConnectionID: row.ConnectionID, Kind: "gitlab", Base: row.ProviderBase, AccountID: row.ProviderAccount}}
	if err := i.scope.validate(); err != nil {
		return attachmentIntent{}, true, err
	}
	if err := json.Unmarshal(row.Parameters, &i.parameters); err != nil {
		return attachmentIntent{}, true, fmt.Errorf("unreadable attachment request: %w", err)
	}
	if len(row.ProviderResult) != 0 {
		if err := json.Unmarshal(row.ProviderResult, &i.result); err != nil {
			return attachmentIntent{}, true, fmt.Errorf("unreadable attachment result: %w", err)
		}
	}
	return i, true, nil
}

func credentialURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.User = url.User("apptown")
	return u.String(), nil
}

var upstreamName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

func (s *RepositoryAttachments) parameters(ctx context.Context, in AttachmentRequest, p project.Project, session ProviderSession, previous *attachmentParameters) (attachmentParameters, error) {
	if in.Create == (in.URL != "") || in.NamespaceID < 0 || (!in.Create && (in.RepositoryName != "" || in.NamespaceID != 0)) {
		return attachmentParameters{}, fault.New(fault.Invalid, "choose an existing repository URL or create a named repository")
	}
	params := attachmentParameters{Create: in.Create, Branch: p.DefaultBranch}
	if previous != nil {
		params.Branch = previous.Branch
	}
	if in.Create {
		params.Name = in.RepositoryName
		params.Namespace = in.NamespaceID
		if params.Name == "" {
			if previous != nil {
				params.Name = previous.Name
			} else {
				params.Name = p.Slug
			}
		}
		if !upstreamName.MatchString(params.Name) {
			return attachmentParameters{}, fault.New(fault.Invalid, "repository name must contain only letters, numbers, dots, hyphens or underscores")
		}
		if params.Namespace == 0 {
			if previous != nil {
				params.Namespace = previous.Namespace
			} else {
				params.Namespace = session.DefaultNamespace
			}
		}
		if params.Namespace == 0 {
			var groups []struct {
				ID int `json:"id"`
			}
			if err := session.Client.Do(ctx, "GET", "/groups?min_access_level=30&per_page=1", nil, &groups); err != nil {
				return attachmentParameters{}, err
			}
			if len(groups) > 0 {
				params.Namespace = groups[0].ID
			}
		}
		if params.Namespace <= 0 {
			return attachmentParameters{}, fault.New(fault.Invalid, "choose a GitLab namespace for the new repository")
		}
	} else {
		location, _, err := gitlab.RepositoryURL(session.Scope.Base, in.URL)
		if err != nil {
			return attachmentParameters{}, err
		}
		params.URL = location
	}
	return params, nil
}

// Attach owns the external create/attach continuation. It requires current
// project authority and never substitutes a display name for an admitted actor.
func (s *RepositoryAttachments) Attach(ctx context.Context, in AttachmentRequest) (RepositoryIdentity, error) {
	caller, err := s.authority.Require(ctx, access.Permission{Capability: access.CapProjectIntegrations, Resource: access.Resource{Type: "project", ProjectID: in.ProjectID}, Context: access.Context{Consequential: true}})
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if caller.Principal.ID.IsZero() {
		return RepositoryIdentity{}, access.ErrPrincipalRequired
	}
	release, err := s.locks.Acquire(ctx, in.ProjectID)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	defer release()
	if err := project.RefuseUnlessActive(ctx, s.db, in.ProjectID); err != nil {
		return RepositoryIdentity{}, err
	}
	p, err := s.projects.ByID(ctx, in.ProjectID)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if p.DefaultBranch == "" {
		return RepositoryIdentity{}, fault.New(fault.Unprocessable, "project has no repository branch to attach")
	}
	repo, err := p.Repository(s.dataDir)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	intent, found, err := s.readIntent(ctx, in.ProjectID)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	var expected *ProviderScope
	var previous *attachmentParameters
	if found {
		if intent.row.ActorID != caller.Principal.ID || (!in.RequestID.IsZero() && intent.row.RequestID != in.RequestID) {
			return RepositoryIdentity{}, fault.New(fault.Conflict, "another attachment request already owns this project's upstream choice")
		}
		expected = &intent.scope
		previous = &intent.parameters
	}
	existing, attached, err := readAttachment(ctx, s.db, in.ProjectID)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if !in.ExpectedAttachmentID.IsZero() && (!attached || existing.AttachmentID != in.ExpectedAttachmentID) {
		return RepositoryIdentity{}, fault.New(fault.Conflict, "repository attachment generation changed")
	}
	if attached && (!found || existing.AttachmentID != intent.row.ID) {
		return RepositoryIdentity{}, fault.New(fault.Conflict, "this project already has a verified repository attachment; replacement is a separate operation")
	}
	session, err := s.snapshot(ctx, expected)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	params, err := s.parameters(ctx, in, p, session, previous)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if found && params != intent.parameters {
		return RepositoryIdentity{}, fault.New(fault.Conflict, "attachment request parameters changed; resume the original upstream choice")
	}
	origin, err := ReadOrigin(ctx, repo)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if attached {
		location, _, locationErr := gitlab.RepositoryURL(session.Scope.Base, origin)
		if locationErr != nil || location != existing.CloneURL {
			return RepositoryIdentity{}, fault.New(fault.Conflict, "repository origin differs from the completed attachment")
		}
		observed, err := session.Client.Project(ctx, strconv.FormatInt(existing.ProviderProjectID, 10))
		if err != nil {
			return RepositoryIdentity{}, err
		}
		observed, err = observeRepository(session, observed)
		if err != nil {
			return RepositoryIdentity{}, err
		}
		if observed.ID != existing.ProviderProjectID || observed.HTTPURL != existing.CloneURL {
			return RepositoryIdentity{}, fault.New(fault.Conflict, "provider repository differs from the completed attachment")
		}
		return existing, nil
	}
	if !found {
		if in.RecoverProviderID != 0 {
			return RepositoryIdentity{}, fault.New(fault.Invalid, "there is no recorded attachment operation to recover")
		}
		if origin != "" {
			location, _, err := gitlab.RepositoryURL(session.Scope.Base, origin)
			if err != nil || params.Create || location != params.URL {
				return RepositoryIdentity{}, fault.New(fault.Conflict, "an existing origin must be verified as itself; it cannot be replaced by a new attachment request")
			}
		}
		id := ids.New(s.now())
		requestID := in.RequestID
		if requestID.IsZero() {
			requestID = id
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return RepositoryIdentity{}, err
		}
		err = s.db.InTx(ctx, func(tx *database.Tx) error {
			if err := project.RefuseUnlessActiveIn(ctx, tx, in.ProjectID); err != nil {
				return err
			}
			n, err := scmsql.New(tx.SQL()).AdmitRepositoryAttachment(ctx, scmsql.AdmitRepositoryAttachmentParams{ID: id, RequestID: requestID, ProjectID: in.ProjectID, ActorID: caller.Principal.ID, Parameters: raw, ConnectionID: session.Scope.ConnectionID, ProviderBase: session.Scope.Base, ProviderAccount: session.Scope.AccountID, PriorOrigin: origin, CreatedAt: s.now()})
			if err != nil {
				return err
			}
			if n != 1 {
				return fault.New(fault.Conflict, "another attachment request was admitted")
			}
			return nil
		})
		if err != nil {
			return RepositoryIdentity{}, err
		}
		intent, found, err = s.readIntent(ctx, in.ProjectID)
		if err != nil {
			return RepositoryIdentity{}, fmt.Errorf("reading admitted attachment: %w", err)
		}
		if !found {
			return RepositoryIdentity{}, fault.New(fault.Conflict, "admitted attachment record is missing")
		}
		if params.Create {
			n, err := scmsql.New(s.db.SQL()).DispatchRepositoryAttachment(ctx, scmsql.DispatchRepositoryAttachmentParams{ID: id, ProjectID: in.ProjectID, UpdatedAt: s.now()})
			if err != nil {
				return RepositoryIdentity{}, err
			}
			if n != 1 {
				return RepositoryIdentity{}, intent.refusal()
			}
			result, err := session.Client.CreateRepository(ctx, params.Name, params.Namespace, intent.association())
			if err != nil {
				return RepositoryIdentity{}, fmt.Errorf("provider create outcome requires inspection for attachment %s: %w", id, err)
			}
			intent.result = result
		} else {
			_, path, err := gitlab.RepositoryURL(session.Scope.Base, params.URL)
			if err != nil {
				return RepositoryIdentity{}, err
			}
			intent.result, err = session.Client.Project(ctx, path)
			if err != nil {
				return RepositoryIdentity{}, err
			}
		}
	} else if len(intent.row.ProviderResult) == 0 {
		if params.Create {
			if in.RecoverProviderID <= 0 {
				return RepositoryIdentity{}, intent.refusal()
			}
			result, err := session.Client.Project(ctx, strconv.FormatInt(in.RecoverProviderID, 10))
			if err != nil {
				return RepositoryIdentity{}, err
			}
			if result.ID != in.RecoverProviderID || result.Description != intent.association() || result.CreatorID != intent.scope.AccountID {
				return RepositoryIdentity{}, fault.New(fault.Conflict, "provider repository has no independent association with this attachment operation")
			}
			intent.result = result
		} else {
			_, path, err := gitlab.RepositoryURL(session.Scope.Base, params.URL)
			if err != nil {
				return RepositoryIdentity{}, err
			}
			intent.result, err = session.Client.Project(ctx, path)
			if err != nil {
				return RepositoryIdentity{}, err
			}
		}
	}
	result, err := observeRepository(session, intent.result)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if !params.Create && result.HTTPURL != params.URL {
		return RepositoryIdentity{}, fault.New(fault.Conflict, "provider identity differs from the requested repository")
	}
	if len(intent.row.ProviderResult) == 0 {
		raw, err := json.Marshal(result)
		if err != nil {
			return RepositoryIdentity{}, err
		}
		n, err := scmsql.New(s.db.SQL()).IdentifyRepositoryAttachment(context.WithoutCancel(ctx), scmsql.IdentifyRepositoryAttachmentParams{ID: intent.row.ID, ProjectID: in.ProjectID, ProviderResult: raw, UpdatedAt: s.now()})
		if err != nil {
			return RepositoryIdentity{}, err
		}
		if n != 1 {
			return RepositoryIdentity{}, intent.refusal()
		}
	} else {
		observed, err := session.Client.Project(ctx, strconv.FormatInt(result.ID, 10))
		if err != nil {
			return RepositoryIdentity{}, err
		}
		observed, err = observeRepository(session, observed)
		if err != nil || observed.ID != result.ID || observed.HTTPURL != result.HTTPURL {
			return RepositoryIdentity{}, fault.New(fault.Conflict, "recorded provider result changed before attachment resumed")
		}
	}
	desired, err := credentialURL(result.HTTPURL)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if origin != "" && origin != intent.row.PriorOrigin && origin != desired {
		return RepositoryIdentity{}, fault.New(fault.Conflict, "repository origin changed during attachment; inspect it before retrying")
	}
	if origin != desired {
		args := []string{"remote", "add", "origin", desired}
		if origin != "" {
			args = []string{"remote", "set-url", "origin", desired}
		}
		if _, err := gitexec.HubRun(ctx, repo, args...); err != nil {
			return RepositoryIdentity{}, fmt.Errorf("installing verified origin: %w", err)
		}
	}
	env, err := session.Client.AskpassEnv(s.dataDir)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if out, err := gitexec.PushVerifiedURL(ctx, repo, desired, params.Branch, env); err != nil {
		return RepositoryIdentity{}, fmt.Errorf("identified upstream %d remains resumable after push failure: %w: %s", result.ID, err, out)
	}
	identity := RepositoryIdentity{AttachmentID: intent.row.ID, ProjectID: in.ProjectID, Scope: session.Scope, ProviderProjectID: result.ID, CloneURL: result.HTTPURL, Path: result.PathNS}
	settle := context.WithoutCancel(ctx)
	err = s.db.InTx(settle, func(tx *database.Tx) error {
		if err := project.RefuseUnlessActiveIn(settle, tx, in.ProjectID); err != nil {
			return err
		}
		if err := recordAttachmentIn(settle, tx, identity, s.now()); err != nil {
			return err
		}
		n, err := scmsql.New(tx.SQL()).CompleteRepositoryAttachment(settle, scmsql.CompleteRepositoryAttachmentParams{ID: intent.row.ID, ProjectID: in.ProjectID, UpdatedAt: s.now()})
		if err != nil {
			return err
		}
		if n != 1 {
			return intent.refusal()
		}
		return audit.At(s.now()).RecordIn(settle, tx, caller.Receipt, in.ProjectID, "project.remote.attached", p.Slug, map[string]any{"identity": identity, "request": intent.row.RequestID})
	})
	return identity, err
}
