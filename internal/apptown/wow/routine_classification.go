package wow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// RoutineTransitions records local human classification, not project approval,
// recommendation, adoption or executable authority.
type RoutineTransitions struct {
	db        *database.DB
	authority access.Authorizer
	identity  *identity.Service
	now       func() time.Time
}

func NewRoutineTransitions(db *database.DB, authority access.Authorizer, people *identity.Service, now func() time.Time) RoutineTransitions {
	return RoutineTransitions{db: db, authority: authority, identity: people, now: clock.Or(now)}
}

type RoutinePublicationConsent struct {
	RequestID   ids.ID                       `json:"requestId"`
	Transition  RoutinePublicationTransition `json:"transition"`
	ShownDigest string                       `json:"shownDigest"`
}
type RoutinePublicationRevocation struct {
	Actor  access.Actor `json:"actor"`
	At     time.Time    `json:"at"`
	Reason string       `json:"reason"`
}
type RoutinePublicationClassification struct {
	ID         ids.ID                        `json:"id"`
	RequestID  ids.ID                        `json:"requestId"`
	Question   RoutinePublicationQuestion    `json:"question"`
	Issuer     access.Actor                  `json:"issuer"`
	At         time.Time                     `json:"at"`
	Revocation *RoutinePublicationRevocation `json:"revocation,omitempty"`
}

func (s RoutineTransitions) human(ctx context.Context, q database.Queryer) (access.Actor, error) {
	c, ok := access.CallerFrom(ctx)
	if !ok || !c.Verified || c.Principal.Kind != "user" || c.Principal.ID.IsZero() || !c.Principal.Actor().Assents() {
		return access.Actor{}, fault.New(fault.Forbidden, "publication classification requires a verified person acting directly")
	}
	if err := identity.RequireEnabledIn(ctx, q, c.Principal); err != nil {
		return access.Actor{}, err
	}
	if s.authority == nil {
		return access.Actor{}, access.ErrAuthorityUnavailable
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapStandardsPacks, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "routine-publication"}}); err != nil {
		return access.Actor{}, err
	}
	return c.Principal.Actor(), nil
}
func (s RoutineTransitions) Preview(ctx context.Context, in RoutinePublicationTransition) (RoutinePublicationQuestion, error) {
	if _, err := s.human(ctx, s.db); err != nil {
		return RoutinePublicationQuestion{}, err
	}
	return routineQuestionIn(ctx, s.db, in)
}
func readRoutineClassification(row wowsql.RoutinePublicationClassification) (RoutinePublicationClassification, error) {
	var out RoutinePublicationClassification
	if err := json.Unmarshal(row.RecordJson, &out); err != nil {
		return out, err
	}
	digest, err := routineQuestionDigest(out.Question)
	if err != nil {
		return out, err
	}
	t := out.Question.Transition
	if out.ID != row.ID || out.RequestID != row.RequestID || out.Issuer.Principal != row.IssuerID || out.At.UTC() != row.CreatedAt.UTC() || t.Kind != row.PublicationKind || t.ID != row.PublicationID || t.From != row.FromVersion || t.To != row.ToVersion || out.Question.Digest != row.Digest || digest != row.Digest || !out.Question.Eligible || out.Revocation != nil {
		return out, fmt.Errorf("publication classification evidence does not match its record")
	}
	if !bytes.Equal(bytes.TrimSpace(row.RevocationJson), []byte("null")) {
		out.Revocation = &RoutinePublicationRevocation{}
		if err = json.Unmarshal(row.RevocationJson, out.Revocation); err != nil {
			return out, err
		}
		if out.Revocation.At.IsZero() || out.Revocation.Reason == "" {
			return out, fmt.Errorf("publication revocation evidence is incomplete")
		}
	}
	return out, nil
}
func (s RoutineTransitions) Classify(ctx context.Context, in RoutinePublicationConsent) (RoutinePublicationClassification, error) {
	if in.RequestID.IsZero() || in.ShownDigest == "" {
		return RoutinePublicationClassification{}, fault.New(fault.Conflict, "retain a request ID and read the exact publication meaning before classifying it")
	}
	if _, err := s.human(ctx, s.db); err != nil {
		return RoutinePublicationClassification{}, err
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (RoutinePublicationClassification, error) {
		actor, err := s.human(ctx, tx)
		if err != nil {
			return RoutinePublicationClassification{}, err
		}
		q := wowsql.New(tx.SQL())
		replay := func() (RoutinePublicationClassification, bool, error) {
			row, e := q.RoutineClassificationByRequest(ctx, wowsql.RoutineClassificationByRequestParams{IssuerID: actor.Principal, RequestID: in.RequestID})
			if errors.Is(e, sql.ErrNoRows) {
				return RoutinePublicationClassification{}, false, nil
			}
			if e != nil {
				return RoutinePublicationClassification{}, false, e
			}
			r, e := readRoutineClassification(row)
			if e != nil {
				return r, true, e
			}
			if r.Question.Transition != in.Transition || r.Question.Digest != in.ShownDigest {
				return r, true, fault.New(fault.Conflict, "this classification request ID was already used for different input")
			}
			return r, true, nil
		}
		if r, found, e := replay(); e != nil || found {
			return r, e
		}
		question, err := routineQuestionIn(ctx, tx, in.Transition)
		if err != nil {
			return RoutinePublicationClassification{}, err
		}
		if !question.Eligible {
			return RoutinePublicationClassification{}, fault.New(fault.Conflict, question.Reason)
		}
		if question.Digest != in.ShownDigest {
			return RoutinePublicationClassification{}, fault.New(fault.Conflict, "the publication meaning changed; read it again before deciding")
		}
		at := s.now().UTC().Truncate(time.Microsecond)
		r := RoutinePublicationClassification{ID: ids.New(at), RequestID: in.RequestID, Question: question, Issuer: actor, At: at}
		raw, err := json.Marshal(r)
		if err != nil {
			return r, err
		}
		_, err = q.InsertRoutineClassification(ctx, wowsql.InsertRoutineClassificationParams{ID: r.ID, PublicationKind: in.Transition.Kind, PublicationID: in.Transition.ID, FromVersion: in.Transition.From, ToVersion: in.Transition.To, Digest: question.Digest, IssuerID: actor.Principal, RequestID: in.RequestID, At: at, RecordJson: raw})
		if errors.Is(err, sql.ErrNoRows) {
			if previous, found, e := replay(); e != nil || found {
				return previous, e
			}
			existing, e := q.ActiveRoutineClassification(ctx, question.Digest)
			if e != nil {
				return r, e
			}
			return r, fault.New(fault.Conflict, "this publication transition already has active classification "+existing.ID.String())
		}
		if err != nil {
			return r, err
		}
		if err = audit.At(at).RecordIn(ctx, tx, actor.Name, ids.Nil, "publication.classified-routine", r.ID.String(), r); err != nil {
			return r, err
		}
		return r, nil
	})
}
func (s RoutineTransitions) History(ctx context.Context, in RoutinePublicationTransition) ([]RoutinePublicationClassification, error) {
	if _, err := s.human(ctx, s.db); err != nil {
		return nil, err
	}
	if in.ID.IsZero() || (in.Kind != "block" && in.Kind != "pack") {
		return nil, fault.New(fault.Conflict, "choose a guidance block or pack identity")
	}
	rows, err := wowsql.New(s.db.SQL()).RoutineClassificationHistory(ctx, wowsql.RoutineClassificationHistoryParams{PublicationKind: in.Kind, PublicationID: in.ID})
	if err != nil {
		return nil, err
	}
	out := make([]RoutinePublicationClassification, 0, len(rows))
	for _, row := range rows {
		r, e := readRoutineClassification(row)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
func revokeRoutineIn(ctx context.Context, tx *database.Tx, id ids.ID, rev RoutinePublicationRevocation) error {
	raw, err := json.Marshal(rev)
	if err != nil {
		return err
	}
	n, err := wowsql.New(tx.SQL()).RevokeRoutineClassification(ctx, wowsql.RevokeRoutineClassificationParams{ID: id, RevocationJson: raw})
	if err != nil || n == 0 {
		return err
	}
	return audit.At(rev.At).RecordIn(ctx, tx, rev.Actor.Name, ids.Nil, "publication.routine-revoked", id.String(), rev)
}
func (s RoutineTransitions) Revoke(ctx context.Context, id ids.ID) (RoutinePublicationClassification, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (RoutinePublicationClassification, error) {
		actor, err := s.human(ctx, tx)
		if err != nil {
			return RoutinePublicationClassification{}, err
		}
		q := wowsql.New(tx.SQL())
		row, err := q.RoutineClassification(ctx, id)
		if err != nil {
			return RoutinePublicationClassification{}, database.Missing(err)
		}
		if _, err = readRoutineClassification(row); err != nil {
			return RoutinePublicationClassification{}, err
		}
		if err = revokeRoutineIn(ctx, tx, id, RoutinePublicationRevocation{Actor: actor, At: s.now().UTC().Truncate(time.Microsecond), Reason: "revoked by a current standards manager"}); err != nil {
			return RoutinePublicationClassification{}, err
		}
		row, err = q.RoutineClassification(ctx, id)
		if err != nil {
			return RoutinePublicationClassification{}, err
		}
		return readRoutineClassification(row)
	})
}

// CheckIn joins an owner's authoritative claim transaction. Lock order is
// project/run/rule, then classification UUIDs. The returned evidence confers no
// other authority; revoke updates these same rows. Assessment uses a short
// transaction and must not later treat its result as an executable grant.
func (s RoutineTransitions) CheckIn(ctx context.Context, tx *database.Tx, wanted []ids.ID) ([]RoutinePublicationClassification, error) {
	if s.identity == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	order := slices.Clone(wanted)
	slices.SortFunc(order, func(a, b ids.ID) int { return a.Compare(b) })
	order = slices.Compact(order)
	out := make([]RoutinePublicationClassification, 0, len(order))
	for _, id := range order {
		row, err := wowsql.New(tx.SQL()).RoutineClassificationForShare(ctx, id)
		if err != nil {
			return nil, database.Missing(err)
		}
		r, err := readRoutineClassification(row)
		if err != nil {
			return nil, err
		}
		if r.Revocation != nil {
			return nil, fault.New(fault.Conflict, "the routine publication classification was revoked")
		}
		if _, err = s.identity.StandingIssuerAllowsIn(ctx, tx, r.Issuer.Principal, access.Permission{Capability: access.CapStandardsPacks, Resource: access.Resource{Type: "instance"}}); err != nil {
			return nil, err
		}
		current, err := routineQuestionIn(ctx, tx, r.Question.Transition)
		if err != nil {
			return nil, err
		}
		if !current.Eligible || current.Digest != r.Question.Digest {
			return nil, fault.New(fault.Conflict, "the classified publication material changed")
		}
		out = append(out, r)
	}
	return out, nil
}

// DisableRestoredRoutineClassifications runs before application recovery under
// the existing exclusive restore hold. Copied consent is history, not local
// authority. Repeating a loaded restore continuation creates no duplicate audit.
func DisableRestoredRoutineClassifications(ctx context.Context, db *database.DB, at time.Time) error {
	return db.InTx(ctx, func(tx *database.Tx) error {
		rows, err := wowsql.New(tx.SQL()).ActiveRoutineClassificationsForUpdate(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if _, err = readRoutineClassification(row); err != nil {
				return err
			}
			if err = revokeRoutineIn(ctx, tx, row.ID, RoutinePublicationRevocation{Actor: access.Actor{Name: "apptown-system"}, At: at.UTC().Truncate(time.Microsecond), Reason: "restored classification requires new local human consent"}); err != nil {
				return err
			}
		}
		return nil
	})
}
