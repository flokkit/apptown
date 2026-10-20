package conversation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/console"
)

// ChatArchive is one ended chat's history as it moves to another AppTown:
// the files named below and nothing else, by their names inside the chat's
// folder.
type ChatArchive struct {
	ID    string
	Files map[string][]byte
}

// ChatExport is a project's chats for a move: the archives, how many ended
// chats had no words (counted, not moved), and the chats that
// stay behind: still live, or on a change that does not move.
type ChatExport struct {
	Chats   []ChatArchive
	Empty   int
	Skipped []SkippedChat
	// EmptyIDs are the chats with no words, which stay.
	EmptyIDs []string
}

// Staying are the ids of every chat that stays: skipped and empty.
func (e ChatExport) Staying() []string {
	out := append([]string{}, e.EmptyIDs...)
	for _, c := range e.Skipped {
		out = append(out, c.ID)
	}
	return out
}

// SkippedChat is a chat that stays on the source AppTown, and why.
type SkippedChat struct {
	ID       string `json:"id"`
	ChangeID ids.ID `json:"changeId,omitzero"`
	Why      string `json:"why"` // live | change-not-moved
}

// ChatRecords reads chats' records on disk, live or ended
// (console.Service.RecordsMatching).
type ChatRecords interface {
	RecordsMatching(m console.Match) ([]console.Recording, []string, error)
}

// ExportChats reads every chat on a project, change chats included, from
// the records on disk, without registering or waking any. An ended chat on
// the project, or on a change that moves, moves as its record (without the
// agent's credential or this host's paths), its words, its pasted pictures,
// its last save and what it was opened with; a running chat's runtime files
// stay. A record that cannot be read is an error, never a chat with no
// words.
func ExportChats(sessions ChatRecords, projectID ids.ID, moves func(changeID ids.ID) bool) (ChatExport, error) {
	records, unreadable, err := sessions.RecordsMatching(console.Match{Labels: map[string]string{ProjectLabel: projectID.String()}})
	if err != nil {
		return ChatExport{}, err
	}
	// A record that cannot be read may be this project's: the move cannot
	// say it is complete, so it refuses, naming the folders.
	if len(unreadable) > 0 {
		return ChatExport{}, fmt.Errorf("the chat records in %s cannot be read, and any of them may be this project's", strings.Join(unreadable, ", "))
	}
	var out ChatExport
	for _, r := range records {
		change := labelID(r.Labels, ChangeLabel)
		switch {
		case !r.Ended:
			out.Skipped = append(out.Skipped, SkippedChat{ID: r.ID, ChangeID: change, Why: "live"})
			continue
		case !change.IsZero() && !moves(change):
			out.Skipped = append(out.Skipped, SkippedChat{ID: r.ID, ChangeID: change, Why: "change-not-moved"})
			continue
		}
		msgs, err := r.ReadMessages()
		if err != nil {
			return ChatExport{}, fmt.Errorf("chat %s: %w", r.ID, err)
		}
		if len(msgs) == 0 {
			out.Empty++
			out.EmptyIDs = append(out.EmptyIDs, r.ID)
			continue
		}
		chat, err := archiveChat(r)
		if err != nil {
			return ChatExport{}, fmt.Errorf("chat %s: %w", r.ID, err)
		}
		out.Chats = append(out.Chats, chat)
	}
	return out, nil
}

func archiveChat(r console.Recording) (ChatArchive, error) {
	meta := r.Meta
	meta.Token, meta.Checkout, meta.OwnsCheckout = "", "", false
	record, err := json.Marshal(meta)
	if err != nil {
		return ChatArchive{}, err
	}
	files := map[string][]byte{console.EndedFile: record}
	add := func(name string) error {
		b, err := os.ReadFile(filepath.Join(r.Dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			files[name] = b
		}
		return err
	}
	if err := add(console.AuthoredFile); err != nil {
		return ChatArchive{}, err
	}
	if err := add(console.SavedFile); err != nil {
		return ChatArchive{}, err
	}
	if raw, ok := files[console.SavedFile]; ok {
		saved, err := withoutKeptWork(raw)
		if err != nil {
			return ChatArchive{}, fmt.Errorf("its last save: %w", err)
		}
		files[console.SavedFile] = saved
	}
	// Each folder is listed with its errors: one that is absent holds
	// nothing, one that cannot be read refuses, never moves as empty.
	for _, folder := range []struct{ name, suffix string }{{"sessions", ".jsonl"}, {"uploads", ""}} {
		entries, err := os.ReadDir(filepath.Join(r.Dir, folder.name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ChatArchive{}, err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), folder.suffix) {
				continue
			}
			if err := add(filepath.Join(folder.name, e.Name())); err != nil {
				return ChatArchive{}, err
			}
		}
	}
	return ChatArchive{ID: r.ID, Files: files}, nil
}

// withoutKeptWork says, in a chat's save record, that a refused save's work
// stayed on the source AppTown: refused work is never carried (it can hold
// the agent's runtime files, credentials among them), so the record names
// the ref it was kept under as keptOnSource, never as kept.
func withoutKeptWork(raw []byte) ([]byte, error) {
	var save map[string]any
	if err := json.Unmarshal(raw, &save); err != nil {
		return nil, err
	}
	kept, ok := save["kept"]
	if !ok {
		return raw, nil
	}
	delete(save, "kept")
	save["keptOnSource"] = kept
	return json.Marshal(save)
}
