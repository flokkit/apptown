package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Every map response with a nil slice at the top answers [] for it.
func TestWriteJSONNeverAnswersNullForAList(t *testing.T) {
	t.Parallel()
	var names []string
	w := httptest.NewRecorder()
	WriteJSON(w, 200, map[string]any{"names": names, "count": 0, "note": nil, "kept": []int{1}})
	body := w.Body.String()
	if !strings.Contains(body, `"names":[]`) || !strings.Contains(body, `"note":null`) || !strings.Contains(body, `"kept":[1]`) {
		t.Fatalf("body: %s", body)
	}
}
