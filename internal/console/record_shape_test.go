package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const piFixture = "testdata/pisession/console.jsonl"

// The reader against a record pi actually wrote, not a handwritten one.
func TestReaderParsesRealShape(t *testing.T) {
	t.Parallel()
	rd := Reader{Path: piFixture}
	entries, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 9 {
		t.Fatalf("want 9 entries, got %d", len(entries))
	}
	for i, typ := range []string{"session", "model_change", "thinking_level_change", "message"} {
		if entries[i].Type != typ {
			t.Errorf("entry %d type: want %s got %s", i, typ, entries[i].Type)
		}
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-05-15T09:58:01.155Z")
	if !entries[3].At.Equal(want) || entries[3].Role != "user" || entries[3].Text != "Hi, can you count the SLOC of c in this project" {
		t.Errorf("user entry parsed wrong: %+v", entries[3])
	}
	if e := entries[4]; e.Role != "assistant" || e.HasText() || len(e.Tools) != 1 || e.Tools[0].Name != "bash" ||
		!strings.HasPrefix(e.Tools[0].Detail(), "cd /work && find") {
		t.Errorf("tool-calling entry parsed wrong: %+v", e)
	}
	if e := entries[5]; e.Role != "toolResult" || !e.HasText() {
		t.Errorf("toolResult entry parsed wrong: %+v", e)
	}
	if e := entries[6]; e.Role != "assistant" || e.HasText() || len(e.Tools) != 0 || e.Usage.Total != 1912 {
		t.Errorf("thinking-only entry parsed wrong: %+v", e)
	}
	if e := entries[8]; e.Provider != "openrouter" || e.Model != "anthropic/claude-sonnet-4" ||
		e.Usage.Total != 2660 || e.Usage.Cost.Total != 0.0012 || !strings.HasPrefix(e.Text, "About 38k") {
		t.Errorf("final entry parsed wrong: %+v", e)
	}
	// Timestamps are times, not strings: the continued-session rule compares
	// them against createdAt, which Go writes in a different layout.
	if !entries[8].At.After(entries[3].At) {
		t.Error("timestamps must order as times")
	}
}

// A lane-continued session's record is not named _console: the session's
// record lookup must still find it (the submit check once missed it).
func TestRecordFollowsARenamedFile(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(piFixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions", "2026-05-15T09-57-43-861Z_console.jsonl")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Session{Meta: Meta{ID: "t", AgentSession: AgentSession}, Dir: dir}
	file, size := s.LatestRecord()
	if file != path || size == 0 {
		t.Fatalf("latest record: %s %d", file, size)
	}
	if s.UserMessageAppended(file, size) {
		t.Fatal("nothing appended yet")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"message","id":"x3","timestamp":"2026-05-15T10:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"go"}]}}` + "\n")
	_ = f.Close()
	if !s.UserMessageAppended(file, size) {
		t.Fatal("a user message past the marker must be seen")
	}
	renamed := filepath.Join(filepath.Dir(path), "2026-05-15T09-57-43-861Z_019e2b12.jsonl")
	_ = os.Rename(path, renamed)
	if got, _ := s.LatestRecord(); got != renamed {
		t.Fatalf("session glob must match any record name, got %q", got)
	}
}

// A turn that ends in a model error becomes a helper bubble saying so, in
// plain words, and counts as handed back: the chat must never look busy
// over a helper that has stopped.
func TestModelErrorSurfacesInChat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec := `{"type":"message","id":"a","timestamp":"2026-09-04T09:57:30.814Z","message":{"role":"user","content":[{"type":"text","text":"Please land this change now."}]}}
{"type":"message","id":"b","timestamp":"2026-09-04T09:57:31.655Z","message":{"role":"assistant","content":[],"provider":"anthropic","model":"claude-opus-5","stopReason":"error","errorMessage":"400 {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Your credit balance is too low to access the Anthropic API.\"},\"request_id\":\"req_1\"}"}}
`
	path := writeSessionRecord(t, dir, "2026-09-04T09-57-00-000Z_console.jsonl", rec)
	s := &Session{Dir: dir}
	msgs := s.Messages()
	if len(msgs) != 2 {
		t.Fatalf("want the user turn and the helper's error, got %+v", msgs)
	}
	last := msgs[1]
	if last.Role != "helper" || !last.Error {
		t.Fatalf("the error must be a helper bubble flagged as an error: %+v", last)
	}
	if !strings.Contains(last.Text, "credit balance is too low") || !strings.Contains(last.Text, "send your message again") {
		t.Fatalf("the bubble should carry the provider's message in plain words: %q", last.Text)
	}
	if strings.Contains(last.Text, `{"type"`) {
		t.Fatalf("raw JSON must not leak into the bubble: %q", last.Text)
	}
	past := time.Date(2026, 9, 4, 9, 58, 0, 0, time.UTC)
	_ = os.Chtimes(path, past, past)
	if !s.HandedBack() {
		t.Fatal("a turn that ended in an error is handed back — nothing is running")
	}
}

// Pressing stop aborts the helper's turn; the chat says so, and an aborted
// turn is handed back like an error.
func TestStopSurfacesInChat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec := `{"type":"message","id":"a","timestamp":"2026-09-04T12:40:00.000Z","message":{"role":"user","content":[{"type":"text","text":"rewrite the game loop"}]}}
{"type":"message","id":"b","timestamp":"2026-09-04T12:41:00.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"..."},{"type":"toolCall","name":"edit","arguments":{}}],"stopReason":"aborted","errorMessage":"Operation aborted"}}
`
	path := writeSessionRecord(t, dir, "2026-09-04T12-40-00-000Z_console.jsonl", rec)
	s := &Session{Dir: dir}
	msgs := s.Messages()
	if len(msgs) != 2 || msgs[1].Role != "helper" || !msgs[1].Stopped || msgs[1].Error {
		t.Fatalf("an abort is a helper 'stopped' bubble, not an error: %+v", msgs)
	}
	if !strings.Contains(msgs[1].Text, "I stopped") {
		t.Fatalf("plain words: %q", msgs[1].Text)
	}
	past := time.Date(2026, 9, 4, 12, 42, 0, 0, time.UTC)
	_ = os.Chtimes(path, past, past)
	if !s.HandedBack() {
		t.Fatal("an aborted turn is handed back")
	}
}

func writeSessionRecord(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions", name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
