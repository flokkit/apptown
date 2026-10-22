package workflow

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

type titleOwner uint8

const (
	titleHuman titleOwner = iota + 1
	titleAwaitingChat
	titleGenerated
)

var errChangeTitle = errors.New("invalid Change title ownership")

func (o titleOwner) Value() (driver.Value, error) {
	switch o {
	case titleHuman:
		return "human", nil
	case titleAwaitingChat:
		return "awaiting_chat", nil
	case titleGenerated:
		return "generated", nil
	default:
		return nil, errChangeTitle
	}
}

func (o *titleOwner) Scan(raw any) error {
	var text string
	switch raw := raw.(type) {
	case string:
		text = raw
	case []byte:
		text = string(raw)
	default:
		return errChangeTitle
	}
	for _, candidate := range []titleOwner{titleHuman, titleAwaitingChat, titleGenerated} {
		value, _ := candidate.Value()
		if value == text {
			*o = candidate
			return nil
		}
	}
	return errChangeTitle
}

type titleAct uint8

const (
	pinTitle titleAct = iota + 1
	nameFromFirstMessage
	reviseTitle
)

type changeTitle struct {
	words string
	owner titleOwner
}

type changeTitleEdit struct {
	act             titleAct
	words, observed string
}

// The only title ownership rule. Human pinning is final for automation, even
// when the words do not change. First-message naming is a one-time claim; a late
// model observation can revise only a still-generated title it actually read.
func (t changeTitle) decide(edit changeTitleEdit) (changeTitle, bool, error) {
	if _, err := t.owner.Value(); err != nil {
		return t, false, err
	}
	words := strings.TrimSpace(edit.words)
	if !utf8.ValidString(words) {
		return t, false, fmt.Errorf("invalid title text")
	}
	switch edit.act {
	case pinTitle:
		if words == "" {
			return t, false, nil
		}
		return changeTitle{words, titleHuman}, true, nil
	case nameFromFirstMessage:
		if t.owner != titleAwaitingChat {
			return t, false, nil
		}
		words = firstMessageTitle(words)
		if words == "" {
			return t, false, nil
		}
		return changeTitle{words, titleGenerated}, true, nil
	case reviseTitle:
		if t.owner != titleGenerated || t.words != edit.observed || words == "" {
			return t, false, nil
		}
		if len(words) > 90 {
			return t, false, fmt.Errorf("generated title exceeds its bound")
		}
		return changeTitle{words, titleGenerated}, true, nil
	default:
		return t, false, errChangeTitle
	}
}

func firstMessageTitle(text string) string {
	title := strings.Join(strings.Fields(text), " ")
	for _, cut := range []string{". ", "? ", "! "} {
		if i := strings.Index(title, cut); i > 0 {
			title = title[:i+1]
			break
		}
	}
	if len(title) > 64 {
		end := 61
		for !utf8.RuneStart(title[end]) {
			end--
		}
		title = strings.TrimSpace(title[:end]) + "…"
	}
	return title
}

// The caller owns the project-first transaction and required operation receipt.
func editChangeTitleIn(ctx context.Context, tx *database.Tx, projectID ids.ID, id ids.ID, edit changeTitleEdit) (bool, error) {
	row, err := workflowsql.New(tx.SQL()).ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: id, ProjectID: projectID})
	if err != nil {
		return false, err
	}
	old := changeTitle{words: row.Title}
	if err := old.owner.Scan(row.TitleOwnership); err != nil {
		return false, err
	}
	next, applied, err := old.decide(edit)
	if err != nil || !applied {
		return false, err
	}
	owner, err := next.owner.Value()
	if err != nil {
		return false, err
	}
	n, err := workflowsql.New(tx.SQL()).EditChangeTitle(ctx, workflowsql.EditChangeTitleParams{Title: next.words, TitleOwnership: owner.(string), ID: id, ProjectID: projectID, OldTitle: old.words, OldTitleOwnership: row.TitleOwnership})
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, errChangeEditConflict
	}
	return true, nil
}

func (s changeStore) nameFromChat(ctx context.Context, projectID ids.ID, id ids.ID, text, who string) (bool, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (bool, error) {
		if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, projectID); err != nil {
			return false, err
		}
		applied, err := editChangeTitleIn(ctx, tx, projectID, id, changeTitleEdit{act: nameFromFirstMessage, words: text})
		if err != nil || !applied {
			return false, err
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, who, projectID, "change.titled", fmt.Sprintf("change %d", id), nil); err != nil {
			return false, err
		}
		return true, nil
	})
}

// NameFromChat gives a change whose title waits for its chat the words of
// the chat's first message; who is the chat's actor. Its receipt carries
// AppTown's clock.
func (s *Service) NameFromChat(ctx context.Context, projectID ids.ID, id ids.ID, text, who string) (bool, error) {
	return (changeStore{s.db, s.clocked}).nameFromChat(ctx, projectID, id, text, who)
}
