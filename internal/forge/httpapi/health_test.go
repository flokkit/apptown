package httpapi

import (
	"net/http/httptest"
	"testing"
)

// The health answer is up or down and nothing else: ok while the database
// answers, 503 once it does not.
func TestHealthIsUpWhileTheDatabaseAnswers(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	h := NewHealth(db.Pool().PingContext)
	w := httptest.NewRecorder()
	h.Healthz(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || w.Body.String() != "ok\n" {
		t.Fatalf("up: %d %q", w.Code, w.Body.String())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.Healthz(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 || w.Body.String() != "database unreachable\n" {
		t.Fatalf("down: %d %q", w.Code, w.Body.String())
	}
}
