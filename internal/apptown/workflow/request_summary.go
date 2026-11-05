package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// requestSummaryOpen excludes accepted work, including acceptance before landing.
func (s *Service) requestSummaryOpen(ctx context.Context, source NarrativeSource) (bool, error) {
	if _, _, err := s.changeScope(ctx, source.Change.ID, actionPublishNarrative); err != nil {
		return false, err
	}
	open, err := workflowsql.New(s.db.SQL()).RequestSummaryOpen(ctx, workflowsql.RequestSummaryOpenParams{ID: source.Change.ID, ProjectID: source.Change.ProjectID})
	return open.Valid && open.Bool, err
}

// publishRequestSummary uses narrative authority and the title owner's rule. The
// source comparison prevents two writers from replacing the same observation.
func (s *Service) publishRequestSummary(ctx context.Context, source NarrativeSource, title string, summary requestSummary) error {
	ch := source.Change
	if _, _, err := s.changeScope(ctx, ch.ID, actionPublishNarrative); err != nil {
		return err
	}
	if strings.TrimSpace(summary.Text) == "" || len(strings.Fields(summary.Text)) > 15 || !utf8.ValidString(summary.Text) || title == "" || len(title) > 90 || len(summary.Messages) == 0 || summary.Model == "" {
		return fmt.Errorf("invalid request summary")
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	old := []byte("{}")
	if ch.RequestSummarySource != nil {
		old, err = json.Marshal(ch.RequestSummarySource)
		if err != nil {
			return err
		}
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := workflowsql.New(tx.SQL())
		if _, err := q.ProjectForUpdate(ctx, ch.ProjectID); err != nil {
			return err
		}
		n, err := q.PublishRequestSummary(ctx, workflowsql.PublishRequestSummaryParams{ID: ch.ID, ProjectID: ch.ProjectID, RequestSummary: raw, OldRequestSummary: old})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNarrativeChanged
		}
		if _, err := editChangeTitleIn(ctx, tx, ch.ProjectID, ch.ID, changeTitleEdit{act: reviseTitle, words: title, observed: ch.Title}); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, "apptown-system", ch.ProjectID, "change.request-summarized", fmt.Sprintf("change %d", ch.ID), summary)
	})
}

// SummarizeRequest generates and publishes a summary of the person's request.
// It checks narrative authority and acceptance before resolving or calling the
// model. Unchanged messages reuse the stored summary. Publication preserves
// human title ownership and commits its receipt atomically. Errors leave the
// previous request text in place; the narrative job treats them as advisory.
func (s *Service) SummarizeRequest(ctx context.Context, source NarrativeSource, messages []RequestMessage, resolve func(context.Context, ids.ID) (string, string, error), models modelcall.Completer) error {
	ch := source.Change
	if resolve == nil || !ch.ChatRequest || len(messages) == 0 || ch.RequestSummarySource != nil && slices.Equal(ch.RequestSummarySource.Messages, messages) {
		return nil
	}
	open, err := s.requestSummaryOpen(ctx, source)
	if err != nil || !open {
		return err
	}
	pattern, slot, err := resolve(ctx, ch.ProjectID)
	if err != nil || pattern == "" {
		return err
	}
	material, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	out, _, err := models.Ask(ctx, modelcall.Call{Purpose: "request-summary", ProjectID: ch.ProjectID, Model: pattern, System: requestSummaryPrompt, User: string(material)})
	if err != nil {
		return err
	}
	var draft struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &draft); err != nil {
		return err
	}
	return s.publishRequestSummary(ctx, source, strings.TrimSpace(draft.Title), requestSummary{Text: strings.TrimSpace(draft.Summary), Slot: slot, Model: pattern, Messages: messages})
}

const requestSummaryPrompt = `Summarize what the person asked for across these ordered conversation messages.
Use only their requests, including later additions and corrections. Do not describe implementation or claim success.
Return only a JSON object with "title" (a short plain label, at most 8 words, no trailing period)
and "summary" (one plain sentence, at most 15 words). Paraphrase the whole request; never truncate or quote its opening.`
