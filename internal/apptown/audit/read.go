package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit/internal/auditsql"
	"github.com/flokkit/apptown/internal/database"
)

// Entry is one ledger line as the ledger page shows it.
type Entry struct {
	ID    ids.ID `json:"id"`
	At    string `json:"at"`
	Actor string `json:"actor"`
	// ActorPrincipal and ActorVia are who answers for the line and how they
	// acted, as written; ActorShown is how the actor reads now (renamed,
	// disabled, removed), filled by whoever shows the line.
	ActorPrincipal ids.ID          `json:"actorPrincipal,omitzero"`
	ActorVia       string          `json:"actorVia,omitempty"`
	ActorShown     string          `json:"actorShown,omitempty"`
	ProjectID      ids.ID          `json:"projectId"`
	ProjectSlug    string          `json:"projectSlug"`
	Kind           string          `json:"kind"`
	Subject        string          `json:"subject"`
	Payload        json.RawMessage `json:"payload"`
}

// Filter narrows a reading of the ledger. A project reads its own lines
// and the instance's (project 0); a kind prefix reads one family of
// receipts ("secret." reads every secret receipt).
type Filter struct {
	ProjectID  ids.ID
	KindPrefix string
	Subject    string // exactly this subject (a target's name, a pack's)
	Limit      int
	// PayloadContains filters subject identity before applying the row limit.
	PayloadContains map[string]any
}

const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// Recent is the ledger newest first under the filter, through q.
func Recent(ctx context.Context, q database.Queryer, f Filter) ([]Entry, error) {
	limit := f.Limit
	if limit < 1 || limit > MaxLimit {
		limit = DefaultLimit
	}
	payload := []byte("{}")
	if f.PayloadContains != nil {
		var err error
		payload, err = json.Marshal(f.PayloadContains)
		if err != nil {
			return nil, err
		}
	}
	rows, err := auditsql.New(q.SQL()).RecentReceipts(ctx, auditsql.RecentReceiptsParams{ProjectID: f.ProjectID, KindPrefix: f.KindPrefix, Subject: f.Subject, RowLimit: int32(limit), PayloadContains: payload})
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, Entry{ID: r.ID, At: r.At.Format(time.RFC3339Nano), Actor: r.Actor, ActorPrincipal: r.ActorPrincipal, ActorVia: r.ActorVia, ProjectID: r.ProjectID, ProjectSlug: r.ProjectSlug, Kind: r.Kind, Subject: r.Subject, Payload: compact(string(r.PayloadJson))})
	}
	return out, err
}

func compact(s string) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return json.RawMessage(s)
	}
	return json.RawMessage(buf.Bytes())
}

// ActorsAmongIn is which of the named people any ledger line names as its
// actor, directly or through a chat, an agent connection or a Git ticket
// ("alice via …"). Identity reads it to keep an account that has acted.
func ActorsAmongIn(ctx context.Context, q database.Queryer, names []string) (map[string]bool, error) {
	rows, err := auditsql.New(q.SQL()).ActorsAmong(ctx, names)
	acted := make(map[string]bool, len(rows))
	for _, n := range rows {
		acted[n] = true
	}
	return acted, err
}

// CreationRequest reads the original request from the project's creation record.
// The caller establishes project read authority, or acts as its background worker.
func CreationRequest(ctx context.Context, q database.Queryer, id ids.ID) (string, error) {
	text, err := auditsql.New(q.SQL()).ProjectCreationRequest(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return text, err
}
