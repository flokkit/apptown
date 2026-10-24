package audit

import (
	"context"
	"encoding/json"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// BestEffort records an informational note: a line that repeats a fact an
// owner's own record already holds (a run, a grant, an adoption revision, a
// commit), or narrates something that is not an act on authority, secrets,
// what runs or work. The owner's record is the evidence; this line is for
// the activity feed, and one that cannot be encoded or written is logged.
// A consequential act's receipt is never a note: it uses RecordIn in the
// act's transaction, or Record before an effect outside the database.
func (at At) BestEffort(ctx context.Context, q database.Queryer, actor string, projectID ids.ID, kind, subject string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("note %s on %s not written: its detail cannot be encoded: %v", kind, subject, err)
		return
	}
	if err := Append(ctx, q, at.String(), actor, projectID, kind, subject, string(body)); err != nil {
		log.Printf("note %s on %s not written: %v", kind, subject, err)
	}
}
