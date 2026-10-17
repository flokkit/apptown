package httpapi

import (
	"context"
	"encoding/json"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/wow"
)

// packNamed reads a published pack: (nil, nil) when there is none, an error
// when the read failed, so a failure is never shown as "no such pack".
func packNamed(ctx context.Context, ways *wow.Service, name string) (*wow.Pack, error) {
	p, err := ways.Pack(ctx, name)
	if found, err := database.Lookup(err); err != nil || !found {
		return nil, err
	}
	return p, nil
}
func encodedJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
