package identity

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/fault"
)

var (
	ErrMemberGrantee = fault.New(fault.Invalid, "grantee (username or group:<name>) required")
	errMemberRole    = fault.New(fault.Invalid, "choose a project role: viewer, contributor, maintainer, approver or owner")
	errMemberSubject = fault.New(fault.Invalid, "a member must be a user or group")
)

// ParseSubject accepts a username or an explicitly prefixed user/group name.
func ParseSubject(grantee string) (subjectType, subject string) {
	if g, ok := strings.CutPrefix(grantee, "group:"); ok {
		return "group", g
	}
	if g, ok := strings.CutPrefix(grantee, "user:"); ok {
		return "user", g
	}
	if grantee == "anonymous" {
		return "anonymous", "anonymous"
	}
	return "user", grantee
}

// AddProjectMember records an explicit role and its informational event. The
// transport must authorize project membership management before calling it.
// User registration and the event remain best-effort; the grant is required.
// Removing a member is revoking their grant (DELETE /authz/grants/{id}).
func (s *Service) AddProjectMember(ctx context.Context, projectID ids.ID, grantee, role, by string) error {
	if strings.TrimSpace(grantee) == "" {
		return ErrMemberGrantee
	}
	rv := access.RoleByName(role)
	if rv == nil || rv.Scope != access.ScopeProject {
		return errMemberRole
	}
	st, subj := ParseSubject(grantee)
	if st != "user" && st != "group" {
		return errMemberSubject
	}
	if st == "user" {
		if err := ValidName(subj); err != nil {
			return err
		}
		if _, err := s.EnsureUser(ctx, subj); err != nil {
			log.Printf("recording user %s: %v", subj, err)
		}
	}
	id, err := s.PutGrant(ctx, Spec{SubjectType: st, Subject: subj, Role: role, Scope: access.ScopeProject, ScopeID: projectID.String(), By: by, Reason: "added on the Members tab"})
	if err != nil {
		log.Printf("grant: %v", err)
		return err
	}
	if id.IsZero() {
		return errors.New("could not record the project role")
	}
	audit.At(clock.Or(s.now)()).BestEffort(ctx, s.db, by, projectID, "project.member-added", grantee, map[string]any{"role": role})
	return nil
}
