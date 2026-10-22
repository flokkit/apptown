package audit

import (
	"context"
	"encoding/json"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// BestEffort records an informational event, preserving the legacy empty-object
// fallback for unencodable payloads. Required receipts must use RecordIn.
func (at At) BestEffort(ctx context.Context, q database.Queryer, actor string, projectID ids.ID, kind, subject string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte("{}")
	}
	if err := Append(ctx, q, at.String(), actor, projectID, kind, subject, string(body)); err != nil {
		log.Printf("best-effort write failed: %v", err)
	}
}
