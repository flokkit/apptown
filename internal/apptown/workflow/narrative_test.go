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
		if (err != nil) != tc.bad || got.title != tc.title || got.Note != tc.note {
			t.Fatalf("parse %q: %+v %v", tc.out, got, err)
		}
	}
	for _, tc := range []struct {
		out, note, gap string
		met            RequestMet
	}{
		{"TITLE: t\nASKED: no — only the tab title changed\n\nThe note.", "The note.", "only the tab title changed", requestMetNo},
		{"TITLE: t\nASKED: partly, the second button is missing\n\nThe note.", "The note.", "the second button is missing", requestMetPartly},
		{"TITLE: t\nASKED: yes\n\nThe note.", "The note.", "", requestMetYes},
		{"TITLE: t\nASKED: Yes. all of it\n\nThe note.", "The note.", "", requestMetYes},
		{"TITLE: t\nASKED: unknown\n\nThe note.", "The note.", "", requestMetUnknown},
		{"TITLE: t\nASKED: probably\n\nThe note.", "The note.", "", requestMetUnknown},
		{"TITLE: t\n\nThe note.", "The note.", "", requestMetUnknown},
		{"The note.", "The note.", "", requestMetUnknown},
	} {
		got, err := ParseNarrative(tc.out)
		if err != nil || got.Note != tc.note || got.met != tc.met || got.gap != tc.gap {
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
		Artifacts: []narrativeArtifact{{Kind: "report", Body: strings.Repeat("a", narrativeBudget+10)}},
	}, "")
	if !strings.Contains(out, body) || !strings.Contains(out, "ARTIFACT report (first 120000 of 120010 bytes shown; the rest was not read)") {
		t.Fatalf("evidence: request whole and artifact truncation stated: %.300s", out)
	}
	withReport := NarrativeEvidence(NarrativeSource{
		Change:      &Change{},
		Runs:        []narrativeRunView{{Name: "build", Steps: []string{"check/tests (command)"}}},
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
	if err != nil || d.met != requestMetUnknown || !strings.Contains(d.gap, "To satisfy it: the rest of the diff") {
		t.Fatalf("unknown lost what would settle it: %+v %v", d, err)
	}
	d, err = ParseNarrative("TITLE: t\nASKED: yes — done\n\nNote")
	if err != nil || d.gap != "" {
		t.Fatalf("a met request carries no gap: %+v %v", d, err)
	}
}

// The judgement concerns the software; workflow progress cannot make it fall short.
func TestNarrativePromptJudgesSoftwareRatherThanPendingProcess(t *testing.T) {
	t.Parallel()
	for _, rule := range []string{
		"Judge the software, not completion of the governing workflow",
		"Steps that have not run yet are never a shortfall",
		"including the approval the reader is deciding and reviews still ahead",
		"Workflow steps only limit what you may demand",
		"For a person's change, TITLE is their request even when FOR is absent",
		"Only for scheduled or system changes without FOR, use the working workflow's stated purpose",
	} {
		if !strings.Contains(strings.Join(strings.Fields(NarrativePrompt), " "), rule) {
			t.Errorf("prompt lacks request judgement rule %q", rule)
		}
	}
}

// Quote attribution is exact and message-local; matching evidence is not enough.
func TestNarrativeShortfallsRequireVerbatimPersonWords(t *testing.T) {
	t.Parallel()
	for _, met := range []RequestMet{requestMetPartly, requestMetNo} {
		for _, tc := range []struct {
			name, quote string
			valid       bool
		}{
			{"exact excerpt", "Bring distant cards into view", true},
			{"missing", "", false},
			{"whitespace", " ", false},
			{"paraphrase", "Bring faraway cards into view", false},
			{"case changed", "bring distant cards into view", false},
			{"across messages", "view. Keep", false},
			{"agent report", "Enable demo board editing", false},
			{"generated title", "Generated label", false},
		} {
			t.Run(string(met)+"/"+tc.name, func(t *testing.T) {
				source := NarrativeSource{Change: &Change{ChatRequest: true, Title: "Generated label"}, Messages: []RequestMessage{{Text: "Bring distant cards into view."}, {Text: "Keep keyboard controls."}}, AgentReport: "Enable demo board editing"}
				draft, err := ParseNarrative("TITLE: label\nASKED: " + string(met) + " — distant cards still cannot be reached. To satisfy it: scroll to the card.\nQUOTE: " + tc.quote + "\n\nThe board changed.")
				if err != nil || draft.gapQuote != strings.TrimSpace(tc.quote) || draft.Note != "The board changed." {
					t.Fatalf("parse: %+v %v", draft, err)
				}
				got, gap, quote := narrativeAssessment(source, draft)
				if tc.valid {
					if got != met || gap != draft.gap || quote != tc.quote {
						t.Fatalf("lost supported shortfall: %s %q %q", got, gap, quote)
					}
				} else if got != requestMetUnknown || gap != "" || quote != "" {
					t.Fatalf("unsupported shortfall published: %s %q %q", got, gap, quote)
				}
			})
		}
	}
}

func TestNarrativeAttributesWrittenRequestsAndSendBacks(t *testing.T) {
	t.Parallel()
	for _, source := range []NarrativeSource{
		{Change: &Change{Title: "Keep keyboard controls"}},
		{Change: &Change{Body: "Keep keyboard controls"}},
		{Change: &Change{ChatRequest: true}, Runs: []narrativeRunView{{Asked: &PersonFeedback{Note: "Keep keyboard controls"}}}},
	} {
		met, _, quote := narrativeAssessment(source, NarrativeDraft{met: requestMetNo, gap: "Keyboard controls are absent.", gapQuote: "Keep keyboard controls"})
		if met != requestMetNo || quote != "Keep keyboard controls" {
			t.Fatalf("person-written request lost: %s %q", met, quote)
		}
	}
}
