package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
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

func TestNarrativeDiffReadsPastTheOldCutAndSaysWhatItLeftOut(t *testing.T) {
	t.Parallel()
	patch := "diff --git a/small.txt b/small.txt\n+tiny\ndiff --git a/big.txt b/big.txt\n+" + strings.Repeat("x", 9000) + "\ndiff --git a/fix.go b/fix.go\n+restored the archived card"
	if got := boundedPatch(patch, narrativeBudget); !strings.Contains(got, "restored the archived card") || strings.Contains(got, "not read") {
		t.Fatalf("a fix past the old 6000-byte cut was dropped or flagged: %.200s", got)
	}
	got := boundedPatch(patch, 5000)
	if !strings.HasPrefix(got, "DIFF: first 1 of 3 files shown; the rest were not read") || !strings.Contains(got, "Unread: a/big.txt b/big.txt; a/fix.go b/fix.go") || strings.Contains(got, "restored the archived card") {
		t.Fatalf("truncation not stated: %.300s", got)
	}
	if got := boundedPatch(patch, 20); !strings.Contains(got, "first 1 of 3 files shown") || !strings.Contains(got, "cut short") || !strings.Contains(got, "a/small.txt b/small.txt; a/big.txt") {
		t.Fatalf("oversize first file: %.300s", got)
	}
}

func TestNarrativeEvidenceStatesUnreadArtifactAndKeepsWholeRequest(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("r", 5000)
	out := NarrativeEvidence(NarrativeSource{
		Change:    &Change{Body: body},
		Artifacts: []NarrativeArtifact{{Kind: "report", Body: strings.Repeat("a", narrativeBudget+10)}},
	}, "")
	if !strings.Contains(out, body) || !strings.Contains(out, "ARTIFACT report (first 120000 of 120010 bytes shown; the rest was not read)") {
		t.Fatalf("evidence: request whole and artifact truncation stated: %.300s", out)
	}
	withReport := NarrativeEvidence(NarrativeSource{
		Change:      &Change{},
		Runs:        []NarrativeRun{{Name: "build", Steps: []string{"check/tests (command)"}}},
		AgentReport: "I reviewed it and found nothing",
	}, "")
	if !strings.Contains(withReport, "REPORTED BY THE AGENT (its own claim, not checked by AppTown)") || !strings.Contains(withReport, "steps of this workflow: check/tests (command)") {
		t.Fatalf("the report must be labelled a claim and the workflow's steps listed: %s", withReport)
	}
	for _, rule := range []string{"was not read", "never say", "unknown", "To satisfy it:", "the agent reports", "workflow has no step for"} {
		if !strings.Contains(NarrativePrompt, rule) {
			t.Errorf("prompt lacks the unread-evidence rule %q", rule)
		}
	}
}

func TestNarrativeKeepsWhatWouldSatisfyForUnknown(t *testing.T) {
	t.Parallel()
	d, err := ParseNarrative("TITLE: t\nASKED: unknown — the rest of the diff was not read. To satisfy it: the rest of the diff, files X and Y.\n\nNote")
	if err != nil || d.Met != RequestMetUnknown || !strings.Contains(d.Gap, "To satisfy it: the rest of the diff") {
		t.Fatalf("unknown lost what would settle it: %+v %v", d, err)
	}
	d, err = ParseNarrative("TITLE: t\nASKED: yes — done\n\nNote")
	if err != nil || d.Gap != "" {
		t.Fatalf("a met request carries no gap: %+v %v", d, err)
	}
}
