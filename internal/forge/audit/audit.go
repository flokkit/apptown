// Package audit owns required append-only operation receipts.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit/internal/auditsql"
	"flokkit.io/forge/internal/stamp"
)

// At writes ledger lines stamped with the time the caller's owner read on
// its own clock for the operation the line records: audit.At(now).RecordIn.
// The ledger has no clock of its own.
type At time.Time

// RecordIn writes an audit row inside a transaction, where the row is part
// of the operation's required record and must commit with it (design/
// postgres-native.md §2.4). It never opens or commits an independent transaction.
func (at At) RecordIn(ctx context.Context, tx *database.Tx, actor string, projectID ids.ID, kind, subject string, payload any) error {
	return at.RecordDecisionIn(ctx, tx, ids.Nil, actor, projectID, kind, subject, payload)
}

// RecordDecisionIn is RecordIn for a line that points at the
// authorization decision it follows from (a denial of a consequential
// act, authorization-capabilities.md §13).
func (at At) RecordDecisionIn(ctx context.Context, tx *database.Tx, decisionID ids.ID, actor string, projectID ids.ID, kind, subject string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	n, err := auditsql.New(tx.SQL()).AppendReceipt(ctx, line(ctx, time.Time(at), at.String(), actor, projectID, kind, subject, body, decisionID))
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("audit %q for project %d was not recorded", kind, projectID)
	}
	return nil
}

// String is the line's stamp as the ledger stores it: UTC, to the second.
func (at At) String() string { return stamp.Of(time.Time(at)).String() }

// Append writes an already encoded, best-effort event. The caller supplies its
// timestamp and handles the error. Consequential commands use RecordIn instead.
// It records what has happened, so it is written though the caller stops.
func Append(ctx context.Context, q database.Queryer, at, actor string, projectID ids.ID, kind, subject, payload string) error {
	minted, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return fmt.Errorf("audit %q: its stamp %q: %w", kind, at, err)
	}
	_, err = auditsql.New(q.SQL()).AppendReceipt(context.WithoutCancel(ctx), line(ctx, minted, at, actor, projectID, kind, subject, json.RawMessage(payload), ids.Nil))
	return err
}

// line is one ledger line as stored: its id minted at its own stamp, and the
// work it names derived once from its words.
func line(ctx context.Context, minted time.Time, at, actor string, projectID ids.ID, kind, subject string, payload json.RawMessage, decisionID ids.ID) auditsql.AppendReceiptParams {
	who := access.ActorFrom(ctx, actor)
	refs, _ := json.Marshal(Refs(kind, actor, subject, payload, ParseIDs))
	if string(refs) == "null" {
		refs = []byte("[]")
	}
	return auditsql.AppendReceiptParams{ID: ids.New(minted), At: at, Actor: actor, ActorPrincipal: who.Principal, ActorVia: who.Via, ProjectID: projectID, Kind: kind, Subject: subject, PayloadJson: payload, AuthorizationDecisionID: decisionID, Refs: refs}
}
