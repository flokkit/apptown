package database

import "encoding/json"

// JSONRows turns rows a query returned as JSON text (to_jsonb(t)::text)
// into raw JSON values, never null: an empty result is an empty list.
func JSONRows(rows []string, err error) ([]json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(rows))
	for i, r := range rows {
		out[i] = json.RawMessage(r)
	}
	return out, nil
}
