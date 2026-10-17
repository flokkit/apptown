package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func TestNarrativeParserRequiresACompleteValidNote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		out, title, note string
		bad              bool
	}{
		{"TITLE: New outcome.\n\n The note. ", "New outcome", "The note.", false},
		{" Plain note ", "", "Plain note", false},
		{"TITLE: \"A title\"\nbody", "A title", "body", false},
		{"TITLE: " + strings.Repeat("x", 91) + "\nbody", "", "body", false},
		{"TITLE: Only a title", "", "", true},
		{" \n ", "", "", true},
		{"bad\xff", "", "", true},
	} {
		got, err := ParseNarrative(tc.out)
		if (err != nil) != tc.bad || got.Title != tc.title || got.Note != tc.note {
			t.Fatalf("parse %q: %+v %v", tc.out, got, err)
		}
	}
	for _, tc := range []struct {
		out, note, gap string
		met            RequestMet
	}{
		{"TITLE: t\nASKED: no — only the tab title changed\n\nThe note.", "The note.", "only the tab title changed", RequestMetNo},
		{"TITLE: t\nASKED: partly, the second button is missing\n\nThe note.", "The note.", "the second button is missing", RequestMetPartly},
		{"TITLE: t\nASKED: yes\n\nThe note.", "The note.", "", RequestMetYes},
		{"TITLE: t\nASKED: Yes. all of it\n\nThe note.", "The note.", "", RequestMetYes},
		{"TITLE: t\nASKED: unknown\n\nThe note.", "The note.", "", RequestMetUnknown},
		{"TITLE: t\nASKED: probably\n\nThe note.", "The note.", "", RequestMetUnknown},
		{"TITLE: t\n\nThe note.", "The note.", "", RequestMetUnknown},
		{"The note.", "The note.", "", RequestMetUnknown},
	} {
		got, err := ParseNarrative(tc.out)
		if err != nil || got.Note != tc.note || got.Met != tc.met || got.Gap != tc.gap {
			t.Fatalf("request reading %q: %+v %v", tc.out, got, err)
		}
	}
	got, err := ParseNarrative(strings.Repeat("界", 1000))
	if err != nil || len(got.Note) != 1998 || !utf8.ValidString(got.Note) {
		t.Fatalf("UTF-8 bound: %d %v", len(got.Note), err)
	}
}

type narrativeNotesFunc func(context.Context, *database.Tx, ids.ID, ids.ID, string) error

func (f narrativeNotesFunc) FillIn(ctx context.Context, tx *database.Tx, p, c ids.ID, n string) error {
	return f(ctx, tx, p, c, n)
}
func TestChangeMergeObservationUsesProjectScope(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	other := workflowTestProject(t, q.db, "other", "Other", "")
	if n, err := workflowsql.New(q.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now().UTC()), RunID: run.ID, At: time.Now().UTC(), Kind: "merge.completed", PayloadJson: json.RawMessage(`{}`)}); err != nil || n != 1 {
		t.Fatalf("store merge observation: %d %v", n, err)
	}
	ctx := context.Background()
	for p, want := range map[ids.ID]bool{run.ProjectID: true, other: false} {
		got, err := ChangeMergedIn(ctx, q.db, p, run.ChangeID)
		if err != nil || got != want {
			t.Fatalf("project %d merged=%v want %v: %v", p, got, want, err)
		}
	}
}
