package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/dataprotection"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/stamp"
	"flokkit.io/forge/stock"
)

// A failed backup answers 502 with its record, and that body is what the
// shipped API description says the operation's 502 is: the description is
// checked against an answer, not only against itself.
func TestAFailedBackupAnswersAsDescribed(t *testing.T) {
	doc := shippedDescription(t)
	described := doc["paths"].(map[string]any)["/api/v1/projects/{slug}/deployments/{name}/backups"].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)["502"]
	if described == nil {
		t.Fatal("the description has no 502 for taking a backup")
	}
	schema := described.(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]

	w := httptest.NewRecorder()
	writeBackupTaken(w, &dataprotection.Backup{ID: idstest.N(1), ProjectID: idstest.N(2), Deployment: "main", TakenAt: "2026-10-13T00:00:00Z",
		Trigger: "manual", Actor: "bls", Status: dataprotection.BackupFailed, Error: "the volume could not be read"})
	if w.Code != 502 {
		t.Fatalf("a failed backup answered %d", w.Code)
	}
	var body any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if err := conforms(doc, schema, body, "body"); err != nil {
		t.Fatalf("%v\n%s", err, w.Body.String())
	}
	// The check is not vacuous: a refusal is not this body.
	if conforms(doc, schema, map[string]any{"error": "x"}, "body") == nil {
		t.Fatal("a refusal conforms to the failed backup's description")
	}
}

// The forge serves the description it ships, to any caller, as JSON.
func TestTheShippedDescriptionIsServed(t *testing.T) {
	w := httptest.NewRecorder()
	NewAPIDescription(stock.FS).Serve(w, httptest.NewRequest("GET", "/api/v1/openapi.json", nil))
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "stock", "docs", "api", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != string(raw) {
		t.Fatalf("served %d %q, %d bytes", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}
}

// A step that has not started or finished answers its times as empty
// strings, and the description allows that: a pending step conforms to the
// Step it describes, as one that ran does.
func TestAStepAnswersAsDescribed(t *testing.T) {
	doc := shippedDescription(t)
	schema := map[string]any{"$ref": "#/components/schemas/Step"}
	ran := workflow.Step{ID: idstest.N(1), RunID: idstest.N(2), Stage: "build", Name: "tests", Type: "tests", Status: workflow.StatusPass}
	ran.StartedAt = stamp.Of(time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC))
	ran.FinishedAt = stamp.Of(time.Date(2026, 10, 13, 1, 3, 3, 0, time.UTC))
	for name, step := range map[string]workflow.Step{
		"pending": {ID: idstest.N(1), RunID: idstest.N(2), Stage: "build", Name: "tests", Type: "tests", Status: workflow.StatusPending},
		"ran":     ran,
	} {
		w := httptest.NewRecorder()
		WriteJSON(w, 200, step)
		var body any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if err := conforms(doc, schema, body, name); err != nil {
			t.Errorf("%v\n%s", err, w.Body.String())
		}
	}
	// The format is checked: an empty string is not a date-time.
	if conforms(doc, map[string]any{"type": "string", "format": "date-time"}, "", "t") == nil {
		t.Fatal("the checker accepts an empty date-time")
	}
}

func shippedDescription(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "stock", "docs", "api", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// conforms checks a decoded JSON value against a schema of the shipped
// description: references, alternatives, types, required properties, items
// and enumerations — the parts the generator writes.
func conforms(doc map[string]any, schema any, v any, at string) error {
	s, _ := schema.(map[string]any)
	if ref, ok := s["$ref"].(string); ok {
		var target any = doc
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			target = target.(map[string]any)[part]
		}
		return conforms(doc, target, v, at)
	}
	if alts, ok := s["anyOf"].([]any); ok {
		for _, alt := range alts {
			if conforms(doc, alt, v, at) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s matches none of its alternatives", at)
	}
	if want, ok := s["const"]; ok && want != v {
		return fmt.Errorf("%s is %v, not %v", at, v, want)
	}
	if words, ok := s["enum"].([]any); ok && !slices.Contains(words, v) {
		return fmt.Errorf("%s is %v, not one of %v", at, v, words)
	}
	if typ, ok := s["type"]; ok && !hasType(typ, v) {
		return fmt.Errorf("%s is %T, not %v", at, v, typ)
	}
	if text, ok := v.(string); ok {
		switch s["format"] {
		case "date-time":
			if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
				return fmt.Errorf("%s is %q, not a date-time", at, text)
			}
		case "uuid":
			if _, err := ids.Parse(text); err != nil {
				return fmt.Errorf("%s is %q, not a UUID", at, text)
			}
		}
	}
	switch value := v.(type) {
	case map[string]any:
		required, _ := s["required"].([]any)
		for _, name := range required {
			if _, ok := value[name.(string)]; !ok {
				return fmt.Errorf("%s has no %s", at, name)
			}
		}
		props, _ := s["properties"].(map[string]any)
		for name, sub := range props {
			if inner, ok := value[name]; ok {
				if err := conforms(doc, sub, inner, at+"."+name); err != nil {
					return err
				}
			}
		}
	case []any:
		for i, item := range value {
			if err := conforms(doc, s["items"], item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasType(typ any, v any) bool {
	names := []any{typ}
	if list, ok := typ.([]any); ok {
		names = list
	}
	for _, name := range names {
		switch name {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "integer", "number":
			if _, ok := v.(float64); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		}
	}
	return false
}
