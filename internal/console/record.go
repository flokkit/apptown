// Package console owns the record of a chat: pi's session files (console.md
// §5a: terminal = presentation, session files = record) read through one
// parser, and the transcript projected from them as chat bubbles, with
// the hand-back signal and the submit verification the runtime relies on.
// The containers, the steering and the HTTP doors stay in internal/apptown; the words
// are read here, the same way whether the session is live or long ended.
package console

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/strs"
)

// Usage is what a model call says it cost: the accounting ledger's
// source.
type Usage struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"totalTokens"`
	Cost       struct {
		Total             float64 `json:"total"`
		TotalFromProvider bool    `json:"totalFromProvider"`
	} `json:"cost"`
}

// PriceSource is Pi's explicit provider-price marker or its calculated model cost.
func (u Usage) PriceSource() string {
	if u.Cost.TotalFromProvider {
		return "provider-reported"
	}
	if u.Cost.Total > 0 {
		return "catalog"
	}
	return "none"
}

// entry is one line of the record, reduced to what AppTown reads. One
// JSON object per line under pi's session directory: a "session" header,
// then "model_change" / "thinking_level_change" marks and "message"
// entries whose role is user, assistant or toolResult.
type entry struct {
	Type     string    // "session" | "model_change" | "message" | ...
	At       time.Time // the line's timestamp; zero when absent or unparsable
	Role     string    // message entries: user | assistant | toolResult
	Provider string    // assistant entries
	Model    string
	Usage    Usage
	Text     string // the text parts, joined by newlines (thinking excluded)
	Tools    []toolCall
	// An assistant entry that ended in an error (the model call failed: no
	// credit, a bad key, a provider outage): stopReason "error" and the raw
	// provider message. Without surfacing this the chat looks busy forever.
	StopReason   string
	ErrorMessage string
	// ID names the entry; a "context_edit" entry names the one it edits in
	// TargetID. Omits: the edit drops its target from what the model sees,
	// which is how pi marks a failed attempt it is retrying.
	ID, TargetID string
	Omits        bool
}

// failed: the helper's turn ended without an answer, a model error or the
// person pressing stop (pi records that as stopReason "aborted").
func (e entry) failed() bool {
	return e.Role == "assistant" && (e.StopReason == "error" || e.StopReason == "aborted")
}

// aborted: the person stopped the turn.
func (e entry) aborted() bool { return e.Role == "assistant" && e.StopReason == "aborted" }

// hasText: the entry says something visible (not just thinking or tools).
func (e entry) hasText() bool { return strings.TrimSpace(e.Text) != "" }

// toolCall is one tool the helper called in an entry.
type toolCall struct {
	Name string
	Args map[string]any
}

// detail picks the one argument worth showing beside a tool's name in a
// chat bubble: the command, the path, the pattern, the URL.
func (t toolCall) detail() string {
	for _, k := range []string{"command", "path", "file_path", "pattern", "url"} {
		if v, ok := t.Args[k].(string); ok {
			return v
		}
	}
	return ""
}

// Reader reads a record incrementally. Off is the byte offset of the first
// unread complete line; a trailing partial line (pi mid-write) is left for
// the next call. Zero value plus a path reads from the start.
type Reader struct {
	Path string
	Off  int64
}

// Next returns the entries in the complete lines appended since the last
// call (all of them on the first). A record shorter than the offset (a
// rewrite, not an append) is read again from the start.
//
// The agent writes its record, so the reader trusts nothing about it: it
// reads only a regular file, opened through a root on the record's directory
// so a link cannot lead it elsewhere, up to the size it had when opened, and
// it skips a line longer than maxLine without holding it.
func (r *Reader) Next() ([]entry, error) {
	f, err := openRecord(r.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < r.Off {
		r.Off = 0
	}
	if _, err := f.Seek(r.Off, io.SeekStart); err != nil {
		return nil, err
	}
	rd := bufio.NewReaderSize(io.LimitReader(f, fi.Size()-r.Off), 64<<10)
	var out []entry
	for {
		line, n, complete := readLine(rd)
		if !complete {
			break // partial trailing line stays for next time
		}
		r.Off += n
		if line != "" {
			if e, ok := parseLine(line); ok {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// maxLine bounds one record line held in memory; a longer one is skipped.
// A variable only so a test can bound it small.
var maxLine = 16 << 20

// readLine reads one line and its length in bytes. A line over maxLine is
// consumed without being kept, and answered as "". complete is false at the
// end of what was read, when the last line has no newline yet.
func readLine(rd *bufio.Reader) (line string, n int64, complete bool) {
	var b strings.Builder
	for {
		chunk, err := rd.ReadSlice('\n')
		n += int64(len(chunk))
		if b.Len()+len(chunk) <= maxLine {
			b.Write(chunk)
		}
		switch {
		case err == nil:
			if n > int64(maxLine) {
				return "", n, true
			}
			return b.String(), n, true
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return "", n, false
		}
	}
}

// openRecord opens a record file that is a regular file directly inside its
// directory: a link out of the directory, or a device, is refused.
func openRecord(path string) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Opened without waiting: the agent can replace a record with a pipe,
	// whose ordinary open waits for a writer; the type is checked after.
	f, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|openNoWait, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a record file", filepath.Base(path))
	}
	return f, nil
}

// parseLine decodes one record line. Lines that are not JSON objects
// (blank, corrupt) are skipped, not errors: the record is append-only and
// a bad line is pi's, not ours.
func parseLine(line string) (entry, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return entry{}, false
	}
	var raw struct {
		Type        string          `json:"type"`
		ID          string          `json:"id"`
		TargetID    string          `json:"targetId"`
		Replacement json.RawMessage `json:"replacement"`
		Timestamp   string          `json:"timestamp"`
		Provider    string          `json:"provider"`
		Model       string          `json:"model"`
		Usage       Usage           `json:"usage"`
		Message     struct {
			Role         string          `json:"role"`
			Provider     string          `json:"provider"`
			Model        string          `json:"model"`
			Usage        Usage           `json:"usage"`
			Content      json.RawMessage `json:"content"`
			StopReason   string          `json:"stopReason"`
			ErrorMessage string          `json:"errorMessage"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &raw) != nil {
		return entry{}, false
	}
	e := entry{Type: raw.Type, Role: raw.Message.Role, Provider: raw.Message.Provider,
		Model: raw.Message.Model, Usage: raw.Message.Usage,
		StopReason: raw.Message.StopReason, ErrorMessage: raw.Message.ErrorMessage,
		ID: raw.ID, TargetID: raw.TargetID, Omits: raw.Type == "context_edit" && string(raw.Replacement) == "null"}
	if raw.Type == "usage" {
		e.Provider, e.Model, e.Usage = raw.Provider, raw.Model, raw.Usage
	}
	if raw.Timestamp != "" {
		e.At, _ = time.Parse(time.RFC3339Nano, raw.Timestamp)
	}
	if len(raw.Message.Content) == 0 {
		return e, true
	}
	var parts []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(raw.Message.Content, &parts) == nil {
		for _, p := range parts {
			switch p.Type {
			case "text":
				if e.Text != "" {
					e.Text += "\n"
				}
				e.Text += p.Text
			case "toolCall":
				t := toolCall{Name: p.Name}
				_ = json.Unmarshal(p.Arguments, &t.Args)
				e.Tools = append(e.Tools, t)
			}
		}
		return e, true
	}
	var text string
	if json.Unmarshal(raw.Message.Content, &text) == nil {
		e.Text = text
	}
	return e, true
}

// --- the transcript: the record as chat bubbles ----------------------------------

// Message is one chat bubble: a projection of the record at story zoom.
// The terminal remains one toggle away as the forensic layer; there is no
// second rendering that could drift.
type Message struct {
	I     int    `json:"i"`
	Role  string `json:"role"` // "you" | "helper"
	Text  string `json:"text,omitempty"`
	Tools []struct {
		Name   string `json:"name"`
		Detail string `json:"detail,omitempty"`
	} `json:"tools,omitempty"`
	At string `json:"at,omitempty"`
	// Error: the helper's turn ended in a model error; Text says what and
	// what to do. The chat must never look busy over a helper that has
	// stopped.
	Error bool `json:"error,omitempty"`
	// Stopped: the person pressed stop and the helper aborted its turn.
	Stopped bool `json:"stopped,omitempty"`
}

// helperErrorText turns a provider's error into a sentence a person can
// act on; retried says pi already tried the turn again (it retries only
// what it judges a passing fault: a dropped stream, an overloaded or
// unreachable provider). Provider JSON ({"error":{"message":…}}) is unwrapped; anything
// else is shown trimmed.
func helperErrorText(raw string, retried bool) string {
	msg := strings.TrimSpace(raw)
	if i := strings.Index(msg, "{"); i >= 0 {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(msg[i:]), &body) == nil {
			if body.Error.Message != "" {
				msg = body.Error.Message
			} else if body.Message != "" {
				msg = body.Message
			}
		}
	}
	msg = strs.Truncate(msg, 300)
	if msg == "" {
		msg = "the model call failed"
	}
	if retried {
		return "My model call failed, and failed again when I retried it, so I've stopped: " + msg + "\n\nWait a moment, then send your message again and I'll pick up from here."
	}
	return "My model call failed, so I've stopped: " + msg + "\n\nOnce that's sorted, send your message again and I'll pick up from here."
}

// Transcript is one chat's record on disk, parsed and kept between polls:
// the reader's offset, the bubbles so far, and the last message entry
// (the hand-back signal). A new record file (a fresh runtime's first
// entry) starts it over. Dir is the session's directory; Since hides the
// turns from before this chat existed (a chat that continues a lane
// agent's own session shows only its own turns; the lane's transcript
// lives on the change page). Zero Since shows everything.
type Transcript struct {
	Dir   string
	since time.Time

	mu   sync.Mutex
	file string
	rd   Reader
	msgs []Message
	last *entry
	err  error // the last read's failure, nil when it read
	// failed is a turn that ended in a model error, held back until the
	// record settles: pi may still drop it to retry (failedID names its
	// entry). retrying: pi dropped one and nothing has followed yet;
	// retried: the next failure is a retry's. stoppedAt: when the person
	// pressed stop, until they start new work; pi records nothing when a
	// stop cancels a retry, and a worker's record can reach AppTown after
	// the stop did.
	failed            *Message
	failedID          string
	retrying, retried bool
	stoppedAt         time.Time
}

// settle is how long the record stays unchanged before the helper's turn
// counts as over: the hand-back, and a failed turn's bubble, wait for it.
const settle = 8 * time.Second

// quiet reports whether the newest record file has been unchanged for the
// settle time.
func (t *Transcript) quiet() bool {
	files, err := t.sessionFiles()
	if err != nil || len(files) == 0 {
		return false
	}
	fi, err := os.Lstat(files[len(files)-1])
	return err == nil && fi.Mode().IsRegular() && clock.System().Sub(fi.ModTime()) >= settle // an mtime: the system clock
}

// bubbles is the conversation so far, with a failed turn once the record
// has settled without pi retrying it. Caller holds t.mu.
func (t *Transcript) bubbles() []Message {
	out := make([]Message, len(t.msgs), len(t.msgs)+1)
	copy(out, t.msgs)
	if t.failed != nil && t.quiet() {
		m := *t.failed
		m.I = len(out)
		out = append(out, m)
	}
	return out
}

// interrupted tells the transcript the person pressed stop. While pi waits
// to retry a failed attempt, a stop cancels the retry and pi writes
// nothing, so the stop is how that turn ends, whenever its record arrives.
// A message the person sends after the stop starts new work.
func (t *Transcript) interrupted() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stoppedAt = clock.System() // compared with the record's timestamps
}

// sessionFiles is every record file this chat owns, oldest first: the
// pinned "console" session or the continued lane agent's own; names are
// timestamp-prefixed, so sorted order is chronological.
// Only regular files count: the agent writes this directory, and a link or
// a device there is not a record. A folder that does not exist holds no
// records; one that cannot be read is an error, never "no records".
func (t *Transcript) sessionFiles() ([]string, error) {
	dir := filepath.Join(t.Dir, "sessions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// refresh reads whatever the newest record file has appended since the
// last call and folds it into the bubbles. Caller holds t.mu.
func (t *Transcript) refresh() {
	// A record that cannot be read is never taken for no record.
	files, err := t.sessionFiles()
	if err != nil {
		t.err = err
		return
	}
	if len(files) == 0 {
		t.err = nil
		return
	}
	latest := files[len(files)-1]
	if t.file != latest {
		t.file, t.rd, t.msgs, t.last = latest, Reader{Path: latest}, nil, nil
		t.failed, t.failedID, t.retrying, t.retried = nil, "", false, false
	}
	entries, err := t.rd.Next()
	t.err = err
	for i := range entries {
		e := entries[i]
		if e.Omits && e.TargetID != "" && e.TargetID == t.failedID {
			// pi is retrying the failed attempt: it was a blip, not where
			// the helper stopped, so it is neither a bubble nor the end of
			// the turn. The terminal keeps it.
			t.failed, t.failedID, t.last, t.retrying, t.retried = nil, "", nil, true, true
			continue
		}
		if e.Type != "message" {
			continue
		}
		// Anything after a failed turn means pi did not retry it.
		if t.failed != nil {
			t.failed.I, t.msgs, t.failed, t.failedID = len(t.msgs), append(t.msgs, *t.failed), nil, ""
		}
		t.last, t.retrying = &e, false
		if e.Role == "user" && e.At.After(t.stoppedAt) {
			t.stoppedAt = time.Time{}
		}
		if !t.since.IsZero() && e.At.Before(t.since) {
			continue // the lane's turns, before this chat existed
		}
		if e.Role != "user" && e.Role != "assistant" {
			continue // toolResults are forensic; the terminal has them
		}
		if e.failed() {
			// The helper's turn ended without an answer: a model error, or
			// the person pressed stop. Say so in the chat, as the helper, so
			// the "building…" state clears and the person knows where things
			// stand.
			m := Message{I: len(t.msgs), Role: "helper", Text: helperErrorText(e.ErrorMessage, t.retried), Error: true}
			if e.aborted() {
				m = Message{I: len(t.msgs), Role: "helper", Stopped: true,
					Text: "I stopped — you pressed stop. Say what you'd rather have and I'll carry on from here."}
			}
			if !e.At.IsZero() {
				m.At = e.At.UTC().Format("2006-01-02T15:04:05.000Z07:00")
			}
			if e.aborted() {
				t.msgs = append(t.msgs, m)
			} else {
				t.failed, t.failedID = &m, e.ID
			}
			t.retried = false
			continue
		}
		if !e.hasText() && len(e.Tools) == 0 {
			continue // pure thinking entries stay under the hood
		}
		m := Message{I: len(t.msgs), Role: map[string]string{"user": "you", "assistant": "helper"}[e.Role], Text: e.Text}
		if !e.At.IsZero() {
			m.At = e.At.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		}
		for _, tc := range e.Tools {
			m.Tools = append(m.Tools, struct {
				Name   string `json:"name"`
				Detail string `json:"detail,omitempty"`
			}{tc.Name, strs.Truncate(tc.detail(), 80)})
		}
		t.msgs, t.retried = append(t.msgs, m), false
	}
}

// messages is the conversation as chat bubbles, oldest first: shared by
// the live chat poll, the change transcript and the narrative (session
// files outlive their containers; the words are part of the change's
// story).
func (t *Transcript) messages() []Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refresh()
	return t.bubbles()
}

// readMessages is Messages, with the failure to read the record when there
// was one: a record that cannot be read is not a chat with no words.
func (t *Transcript) readMessages() ([]Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refresh()
	if t.err != nil {
		return nil, fmt.Errorf("reading the chat's record in %s: %w", t.Dir, t.err)
	}
	return t.bubbles(), nil
}

// TurnHandedBack reports whether the helper has finished its turn: the
// latest record entry is an assistant message with text and no tool
// calls, and the transcript has been quiet for a few seconds. This is the
// same structural signal the UI's green bubble uses; the moment the
// helper claims "done" is exactly the moment worth verifying.
func (t *Transcript) TurnHandedBack() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refresh()
	if t.retrying && !t.stoppedAt.IsZero() {
		return true
	}
	if !t.quiet() {
		return false
	}
	last := t.last
	return last != nil && last.Role == "assistant" && (last.failed() || (last.hasText() && len(last.Tools) == 0))
}

// LatestSessionFile returns the newest session record and its size: the
// before/after marker the submit path uses to verify a message landed.
func (t *Transcript) LatestSessionFile() (string, int64) {
	files, err := t.sessionFiles()
	if err != nil || len(files) == 0 {
		return "", 0
	}
	latest := files[len(files)-1]
	fi, err := os.Lstat(latest)
	if err != nil || !fi.Mode().IsRegular() {
		return latest, 0
	}
	return latest, fi.Size()
}

// UserMessageAppended: has a user message entered the record since the
// (file, size) marker? A new file (a fresh runtime's first entry) counts
// from its start.
func (t *Transcript) UserMessageAppended(file string, off int64) bool {
	cur, _ := t.LatestSessionFile()
	if cur == "" {
		return false
	}
	if cur != file {
		off = 0
	}
	rd := Reader{Path: cur, Off: off}
	entries, err := rd.Next()
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type == "message" && e.Role == "user" {
			return true
		}
	}
	return false
}
