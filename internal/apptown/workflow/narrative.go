package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/strs"
)

type NarrativeRun struct {
	// Steps are the governing workflow's steps ("stage/name (type)"): what the
	// work was actually required to pass.
	Steps []string
	ID    ids.ID
	Name  string
	State RunState
}

type NarrativeArtifact struct{ Kind, Body string }

type NarrativeSource struct {
	Change              *Change
	Slug, DefaultBranch string
	Runs                []NarrativeRun
	Artifacts           []NarrativeArtifact
	// AgentReport is what the agent said about its own work when it handed
	// the change off. It is a claim, never evidence.
	AgentReport string
	// Stat and Patch are the committed diff: the stat whole, the patch bounded
	// by narrativeBudget when laid out.
	Stat, Patch string
}

// agentReportKind is the artifact kind of an agent's own report on its work.
// No gate or requirement reads it (the artifact queries skip it): it is
// shown as reported, and the summary reads it as a claim.
const agentReportKind = "agent-reported"

// narrativeBudget is the most bytes of any one evidence block (the committed
// diff, one artifact) handed to the writer. It is sized for the fast model's
// context (about 30k tokens per block), so ordinary changes are read whole;
// whatever is cut is said so in the evidence.
const narrativeBudget = 120_000

// EmptyCommittedChange is the factual note shared by the change page and narrative.
const EmptyCommittedChange = "No committed changes since this change started."

// NarrativeDraft is the writer's output: a title, the note, and its reading
// of whether the work does what was asked (Met, with Gap saying what falls
// short). A draft that does not say is unknown, never met.
type NarrativeDraft struct {
	Title, Note string
	Met         RequestMet
	Gap         string
}

type NarrativePublication struct {
	Source      NarrativeSource
	Draft       NarrativeDraft
	Slot, Model string
}

// NarrativeSource and PublishNarrative are the release-notes writer's
// reads and its one write. The source is read only to write a narrative, and
// writing one spends a model call between the two, so the source asks for
// the write's authority: a caller who may not publish is refused before
// anything is spent. Publishing needs change:write-narrative, which the
// AppTown's own engine holds and which reaches the summary only, never the
// person-written brief; the ledger keeps the narrative as AppTown's.
func (s *Service) NarrativeSource(ctx context.Context, changeID ids.ID) (NarrativeSource, error) {
	if _, _, err := s.changeScope(ctx, changeID, actionPublishNarrative); err != nil {
		return NarrativeSource{}, err
	}
	return s.narratives.Source(ctx, changeID)
}

func (s *Service) PublishNarrative(ctx context.Context, in NarrativePublication) error {
	if _, _, err := s.changeScope(ctx, in.Source.Change.ID, actionPublishNarrative); err != nil {
		return err
	}
	return s.narratives.Publish(ctx, in)
}

var ErrNarrativeChanged = fault.New(fault.Conflict, "Change narrative source changed; refresh again")

// maxGap bounds the gap line: what falls short, then what would satisfy it.
const maxGap = 700

var errNarrativeEmpty = errors.New("model returned no narrative")

type narrativeReceipt struct {
	Slot  string `json:"slot"`
	Model string `json:"model"`
	Chars int    `json:"chars"`
}

// Model output is interpreted before any write. A title without a note is not a
// publication. Bound stored UTF-8 text without cutting through a code point.
// An "ASKED:" line after the title is the writer's reading of the work
// against the request; without one, or with a word it does not know, the
// answer is unknown.
func ParseNarrative(out string) (NarrativeDraft, error) {
	draft := NarrativeDraft{Note: strings.TrimSpace(out), Met: RequestMetUnknown}
	if rest, ok := strings.CutPrefix(draft.Note, "TITLE:"); ok {
		title, body, _ := strings.Cut(rest, "\n")
		title = strings.Trim(strings.TrimSpace(title), `"'.`)
		if len(title) <= 90 {
			draft.Title = title
		}
		draft.Note = strings.TrimSpace(body)
	}
	if rest, ok := strings.CutPrefix(draft.Note, "ASKED:"); ok {
		line, body, _ := strings.Cut(rest, "\n")
		word, gap, _ := strings.Cut(strings.TrimSpace(line), " ")
		draft.Met = parseRequestMet(strings.Trim(word, ".,:;—-"))
		if draft.Met != RequestMetYes {
			draft.Gap = bound(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(gap), "—–-:")), maxGap)
		}
		draft.Note = strings.TrimSpace(body)
	}
	if draft.Note == "" {
		return NarrativeDraft{}, errNarrativeEmpty
	}
	if !utf8.ValidString(draft.Title) || !utf8.ValidString(draft.Note) || !utf8.ValidString(draft.Gap) {
		return NarrativeDraft{}, fmt.Errorf("invalid narrative text")
	}
	draft.Note = bound(draft.Note, 2000)
	return draft, nil
}

// NarrativePrompt is the writer's instruction: the output shape
// ParseNarrative reads (a TITLE line, an ASKED line, a blank line, then the
// "So far" note) and what the note must say.
const NarrativePrompt = `You maintain the record of a software change. Output EXACTLY this shape:
line 1: TITLE: a short label for the change, at most 8 plain words,
naming the outcome (what changed in the software), never quoting the
request verbatim, no trailing period.
line 2: ASKED: then ONE word — yes, partly, no or unknown — saying
whether the software now does what the request (FOR) asked, judged from
the diff and evidence, not from anyone's claim of success; after "partly"
or "no", one plain sentence naming what was asked and is not done (e.g.
"ASKED: no — the visible heading was asked for; only the browser tab
title changed"). Say unknown when nothing says what was asked or the
evidence cannot show it. After "partly", "no" or "unknown", ALWAYS add a
second sentence beginning "To satisfy it:" naming concrete evidence or
an action that would settle it (e.g. "To satisfy it: a test that
restores an archived Done card", "To satisfy it: the rest of the diff,
files X and Y", "To satisfy it: the agent's review report, sent with
request_change_landing"). Judge against the request and the workflow
that governed the change (its steps are listed in the evidence): never
list something as missing, such as an independent review, that the
workflow has no step for. If the evidence says part of the diff or an
artifact was not read, you have not seen all of the work: never say
"partly" or "no" because something is absent from what you read when it
could be in the unread part. Say yes only if what you read
satisfies the whole request; otherwise unknown. Say "partly" or "no" only
for a shortfall the text you did read positively shows.
line 3: blank.
then the "So far" note: answer ONE question for the person who asked —
how is the software different now, and what, if anything, is unfinished
or newly discovered? 3-6 plain sentences. Describe behavior someone
using the app could notice. THEN, if the work DEVIATED from what was
asked or from the project's stated way of doing things — a different
database or technology than agreed, a dropped or substituted feature, a
workaround forced by circumstances — say so plainly and say why in one
sentence each: deviations are the most important sentences in this note
and must never be omitted. Technology substitutions (e.g. "uses SQLite
instead of the Postgres this project prefers, because X") count as
deviations, not machinery. NEVER mention runs, run numbers, commits,
hashes, workspaces, seeding, sweeping, or AppTown internals. A section
headed "REPORTED BY THE AGENT" is the agent's own claim about its work,
not checked by AppTown: it never makes the answer yes by itself, and
when you use it say "the agent reports…". No hype, no
headings, no bullets, no "I".`

// NarrativeEvidence lays out what the writer reads about a change: its
// title, state and brief, its runs oldest first, its artifacts, then extra
// evidence the caller gathered (the diff and chat reports).
func NarrativeEvidence(source NarrativeSource, extra string) string {
	ch := source.Change
	var evidence strings.Builder
	fmt.Fprintf(&evidence, "TITLE: %s\nSTATE: %s\n", ch.Title, ch.State)
	if strings.TrimSpace(ch.Body) != "" {
		fmt.Fprintf(&evidence, "FOR:\n%s\n", ch.Body)
	}
	// Runs are oldest first; reports keep the existing newest-two selection.
	evidence.WriteString("RUNS:\n")
	for _, run := range source.Runs {
		fmt.Fprintf(&evidence, "- run %d %s: %s\n", run.ID, run.Name, run.State)
		if len(run.Steps) > 0 {
			fmt.Fprintf(&evidence, "  steps of this workflow: %s\n", strings.Join(run.Steps, ", "))
		}
	}
	if report := source.AgentReport; strings.TrimSpace(report) != "" {
		note := ""
		if len(report) > narrativeBudget {
			note = fmt.Sprintf(" (first %d of %d bytes shown; the rest was not read)", narrativeBudget, len(report))
			report = strs.Truncate(report, narrativeBudget)
		}
		fmt.Fprintf(&evidence, "REPORTED BY THE AGENT (its own claim, not checked by AppTown)%s:\n%s\n", note, report)
	}
	for _, artifact := range source.Artifacts {
		if strings.TrimSpace(artifact.Body) != "" {
			body := artifact.Body
			note := ""
			if len(body) > narrativeBudget {
				note = fmt.Sprintf(" (first %d of %d bytes shown; the rest was not read)", narrativeBudget, len(body))
				body = strs.Truncate(body, narrativeBudget)
			}
			fmt.Fprintf(&evidence, "ARTIFACT %s%s:\n%s\n", artifact.Kind, note, body)
		}
	}
	if source.Patch != "" {
		fmt.Fprintf(&evidence, "DIFF STAT:\n%s\n%s\n", source.Stat, boundedPatch(source.Patch, narrativeBudget))
	}
	evidence.WriteString(extra)
	return evidence.String()
}

// boundedPatch lays out the patch for the writer in whole files while they fit
// the budget. When files are left out it says which, so the writer knows it
// has not seen all of the work and does not report it as missing.
func boundedPatch(patch string, budget int) string {
	if len(patch) <= budget {
		return "DIFF:\n" + patch
	}
	var files []string
	for rest := patch; rest != ""; {
		next := strings.Index(rest[1:], "\ndiff --git ")
		if next < 0 {
			files, rest = append(files, rest), ""
			continue
		}
		files, rest = append(files, rest[:next+1]), rest[next+2:]
	}
	var shown strings.Builder
	n := 0
	for _, f := range files {
		if shown.Len()+len(f) > budget {
			break
		}
		shown.WriteString(f)
		shown.WriteString("\n")
		n++
	}
	cut := false
	if n == 0 {
		// One file alone exceeds the budget: read the front of it.
		shown.WriteString(strs.Truncate(files[0], budget))
		n, cut = 1, true
	}
	var unread []string
	for i, f := range files {
		if i > n-1 || (cut && i == n-1) {
			line, _, _ := strings.Cut(f, "\n")
			unread = append(unread, strings.TrimPrefix(line, "diff --git "))
		}
	}
	note := fmt.Sprintf("DIFF: first %d of %d files shown; the rest were not read", n, len(files))
	if cut {
		note += " (the last file shown is cut short)"
	}
	return note + ". Unread: " + strings.Join(unread, "; ") + ".\n" + shown.String()
}

// bound cuts text to at most n bytes without splitting a code point.
func bound(text string, n int) string {
	if len(text) <= n {
		return text
	}
	end := n
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func (s narratives) Source(ctx context.Context, id ids.ID) (NarrativeSource, error) {
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return NarrativeSource{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var source NarrativeSource
	source.Change, err = readChange(ctx, tx, id)
	if err != nil {
		return NarrativeSource{}, err
	}
	records := workflowsql.New(tx.SQL())
	project, err := records.ProjectByID(ctx, source.Change.ProjectID)
	if err != nil {
		return NarrativeSource{}, err
	}
	source.Slug, source.DefaultBranch = project.Slug, project.DefaultBranch
	runs, err := records.NarrativeRunsMeta(ctx, workflowsql.NarrativeRunsMetaParams{ChangeID: id, ProjectID: source.Change.ProjectID})
	if err != nil {
		return NarrativeSource{}, database.Missing(err)
	}
	source.Runs = make([]NarrativeRun, 0, len(runs))
	for _, row := range runs {
		state, err := ParseRunState(row.Status)
		if err != nil {
			return NarrativeSource{}, err
		}
		steps, err := records.RunSteps(ctx, workflowsql.RunStepsParams{RunID: row.ID, ProjectID: source.Change.ProjectID})
		if err != nil {
			return NarrativeSource{}, err
		}
		names := make([]string, 0, len(steps))
		for _, st := range steps {
			names = append(names, fmt.Sprintf("%s/%s (%s)", st.Stage, st.Name, st.Type))
		}
		source.Runs = append(source.Runs, NarrativeRun{ID: row.ID, Name: row.WorkflowName, State: state, Steps: names})
	}
	artifacts, err := records.NarrativeArtifacts(ctx, workflowsql.NarrativeArtifactsParams{ChangeID: id, ProjectID: source.Change.ProjectID})
	if err != nil {
		return NarrativeSource{}, err
	}
	source.Artifacts = make([]NarrativeArtifact, 0, len(artifacts))
	for _, row := range artifacts {
		source.Artifacts = append(source.Artifacts, NarrativeArtifact{Kind: row.Kind, Body: row.Body})
	}
	report, err := records.NarrativeAgentReport(ctx, workflowsql.NarrativeAgentReportParams{ChangeID: id, ProjectID: source.Change.ProjectID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return NarrativeSource{}, err
	}
	source.AgentReport = report
	if err = tx.Commit(); err != nil {
		return NarrativeSource{}, err
	}
	return source, nil
}

// This is replaceable prose, not a recovery receipt. The source is an observation;
// publication rechecks its scope and description/source facts. A concurrent human
// title is preserved. The summary comparison prevents overwriting a different
// completed publication, without claiming a durable generation/freshness protocol.
func (s narratives) Publish(ctx context.Context, in NarrativePublication) error {
	source, draft, slot, model := in.Source, in.Draft, in.Slot, in.Model
	if source.Change == nil {
		return fmt.Errorf("invalid narrative publication")
	}
	observed := *source.Change
	ch := &observed
	if ch.ID.IsZero() || ch.ProjectID.IsZero() || strings.TrimSpace(draft.Note) == "" || len(draft.Note) > 2000 || len(draft.Title) > 90 || len(draft.Gap) > maxGap || !utf8.ValidString(draft.Gap) || !utf8.ValidString(draft.Note) || !utf8.ValidString(draft.Title) {
		return fmt.Errorf("invalid narrative publication")
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		records := workflowsql.New(tx.SQL())
		project, err := records.ProjectForUpdate(ctx, ch.ProjectID)
		if err != nil {
			return err
		}
		if project.Slug != source.Slug || project.DefaultBranch != source.DefaultBranch {
			return ErrNarrativeChanged
		}
		met := parseRequestMet(string(draft.Met))
		gap := ""
		if met != RequestMetYes {
			gap = draft.Gap
		}
		n, err := records.PublishChangeNarrative(ctx, workflowsql.PublishChangeNarrativeParams{Summary: draft.Note, RequestMet: string(met), RequestGap: gap, ID: ch.ID, ProjectID: ch.ProjectID, Body: ch.Body, State: ch.State.String(), RunID: ch.RunID, Branch: ch.Branch, BaseSha: ch.BaseSHA, OldSummary: ch.Summary})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNarrativeChanged
		}
		if _, err := editChangeTitleIn(ctx, tx, ch.ProjectID, ch.ID, changeTitleEdit{act: reviseTitle, words: draft.Title, observed: ch.Title}); err != nil {
			return err
		}
		if s.notes == nil {
			return fmt.Errorf("release-note publication is unavailable")
		}
		if err := s.notes.FillIn(ctx, tx, ch.ProjectID, ch.ID, draft.Note); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, "apptown-system", ch.ProjectID, "change.narrative-updated", fmt.Sprintf("change %d", ch.ID), narrativeReceipt{slot, model, len(draft.Note)})
	})
}

// NarrativeReleaseNotes publishes linked Delivery prose in the Change transaction.
type NarrativeReleaseNotes interface {
	FillIn(context.Context, *database.Tx, ids.ID, ids.ID, string) error
}

type narratives struct {
	db    *database.DB
	notes NarrativeReleaseNotes
	clocked
}

func newNarratives(db *database.DB, notes NarrativeReleaseNotes) narratives {
	return narratives{db: db, notes: notes}
}
