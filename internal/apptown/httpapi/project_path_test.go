package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/dbtest"
)

// A project path that names nothing is 404, and a project read that fails is
// a 500: before the owner classified absence, every failed read answered
// "project not found", which hid outages behind a plausible refusal.
func TestProjectFromPathSeparatesAbsenceFromFailure(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	httpTestProject(t, db, "present", "Present")
	projects := project.New(db, nil)
	read := func(slug string) (int, bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+slug, nil)
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		_, ok := ProjectFromPath(projects, w, r)
		return w.Code, ok
	}
	if code, ok := read("present"); !ok || code != http.StatusOK {
		t.Fatalf("present project: %d %v", code, ok)
	}
	if code, ok := read("absent"); ok || code != http.StatusNotFound {
		t.Fatalf("absent project: %d %v", code, ok)
	}
	t.Cleanup(db.FailNextStatement("-- name: ProjectBySlug"))
	if code, ok := read("present"); ok || code != http.StatusInternalServerError {
		t.Fatalf("failed project read: %d %v", code, ok)
	}
}

// The same holds for a change: an id the workflow owner reports absent is
// 404 "no such change", and a read that fails is a 500, never a missing
// change.
func TestChangeFromPathSeparatesAbsenceFromFailure(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	read := func() (int, string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/changes/00000000-270f-7000-8000-000000000000", nil)
		r.SetPathValue("id", "00000000-270f-7000-8000-000000000000")
		w := httptest.NewRecorder()
		if _, ok := ChangeFromPath(db, w, r); ok {
			t.Fatal("an absent change was found")
		}
		return w.Code, w.Body.String()
	}
	if code, body := read(); code != http.StatusNotFound || !strings.Contains(body, "no such change") {
		t.Fatalf("absent change: %d %s", code, body)
	}
	dbtest.FailNextStatement(t, db, "-- name: ChangeByID")
	if code, body := read(); code != http.StatusInternalServerError {
		t.Fatalf("failed change read: %d %s", code, body)
	}
}

// A change is read on its request's context: a request whose caller has
// gone reads nothing and answers no change, rather than finishing the
// read for nobody.
func TestChangeFromPathStopsWithItsRequest(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/changes/00000000-270f-7000-8000-000000000000", nil)
	r.SetPathValue("id", "00000000-270f-7000-8000-000000000000")
	w := httptest.NewRecorder()
	if _, ok := ChangeFromPath(db, w, r); ok {
		t.Fatal("a change was read for a request that had gone")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("a gone request's read: %d %s", w.Code, w.Body.String())
	}
}
