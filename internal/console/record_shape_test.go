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
	if len(entries) != 11 {
		t.Fatalf("want 11 entries, got %d", len(entries))
	}
	for i, typ := range []string{"session", "model_change", "thinking_level_change", "message"} {
		if entries[i].Type != typ {
			t.Errorf("entry %d type: want %s got %s", i, typ, entries[i].Type)
		}
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-11-04T09:05:28.288Z")
	if !entries[4].At.Equal(want) || entries[4].Role != "user" || entries[4].Text != "Run the fixture tool." {
		t.Errorf("user entry parsed wrong: %+v", entries[4])
	}
	if e := entries[5]; e.Role != "assistant" || e.hasText() || len(e.Tools) != 2 || e.Tools[0].Name != "bash" ||
		!strings.HasPrefix(e.Tools[0].detail(), "printf") {
		t.Errorf("tool-calling entry parsed wrong: %+v", e)
	}
	if e := entries[6]; e.Role != "toolResult" || !e.hasText() {
		t.Errorf("toolResult entry parsed wrong: %+v", e)
	}
	if e := entries[8]; e.Role != "assistant" || e.hasText() || len(e.Tools) != 0 || e.Usage.Total != 125 {
		t.Errorf("thinking-only entry parsed wrong: %+v", e)
	}
	if e := entries[10]; e.Provider != "fixture" || e.Model != "pi-upgrade" ||
		e.Usage.Total != 148 || e.Usage.Cost.Total != 0.000156 || e.Text != "The fixture completed." {
		t.Errorf("final entry parsed wrong: %+v", e)
	}
	edit := entries[5].Tools[1]
	edits, ok := edit.Args["edits"].([]any)
	if edit.Name != "edit" || edit.detail() != "fixture.txt" || !ok || len(edits) != 1 {
		t.Fatalf("edit arguments: %+v", edit)
	}
	change := edits[0].(map[string]any)
	if change["oldText"] != "before" || change["newText"] != "after" {
		t.Fatalf("edit replacement: %v", change)
	}
	if entries[7].Role != "toolResult" || !entries[7].hasText() {
		t.Fatalf("edit result: %+v", entries[7])
	}
	// Timestamps are times, not strings: the continued-session rule compares
	// them against createdAt, which Go writes in a different layout.
	if !entries[10].At.After(entries[4].At) {
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
	if s.record().UserMessageAppended(file, size) {
		t.Fatal("nothing appended yet")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"message","id":"x3","timestamp":"2026-05-15T10:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"go"}]}}` + "\n")
	_ = f.Close()
	if !s.record().UserMessageAppended(file, size) {
		t.Fatal("a user message past the marker must be seen")
	}
	renamed := filepath.Join(filepath.Dir(path), "2026-05-15T09-57-43-861Z_019e2b12.jsonl")
	_ = os.Rename(path, renamed)
	if got, _ := s.LatestRecord(); got != renamed {
		t.Fatalf("session glob must match any record name, got %q", got)
	}
}

// A turn that ends in a model error becomes a helper bubble saying so, in
// plain words, once the record has settled (pi may still retry it), and
// counts as handed back: the chat must never look busy over a helper that
// has stopped.
func TestModelErrorSurfacesInChat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rec := `{"type":"message","id":"a","timestamp":"2026-09-04T09:57:30.814Z","message":{"role":"user","content":[{"type":"text","text":"Please land this change now."}]}}
{"type":"message","id":"b","timestamp":"2026-09-04T09:57:31.655Z","message":{"role":"assistant","content":[],"provider":"anthropic","model":"claude-opus-5","stopReason":"error","errorMessage":"400 {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Your credit balance is too low to access the Anthropic API.\"},\"request_id\":\"req_1\"}"}}
`
	path := writeSessionRecord(t, dir, "2026-09-04T09-57-00-000Z_console.jsonl", rec)
	s := &Session{Dir: dir}
	if msgs := s.messages(); len(msgs) != 1 {
		t.Fatalf("before the record settles, the failure may yet be retried: %+v", msgs)
	}
	past := time.Date(2026, 9, 4, 9, 58, 0, 0, time.UTC)
	_ = os.Chtimes(path, past, past)
	msgs := s.messages()
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
	msgs := s.messages()
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

// A dropped model stream that pi retries is a blip, not a stop. Read as pi
// writes it: the failed attempt alone says nothing yet; pi's edit dropping
// it leaves the turn working through the backoff; when the retry fails
// too, the one bubble says it was retried, and never that the model could
// not be reached.
func TestRetriedModelErrorIsNotAStop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeSessionRecord(t, dir, "2026-11-12T00-11-00-000Z_console.jsonl",
		`{"type":"message","id":"a","timestamp":"2026-11-12T00:11:00.000Z","message":{"role":"user","content":[{"type":"text","text":"add a login page"}]}}
{"type":"message","id":"b","parentId":"a","timestamp":"2026-11-12T00:11:54.000Z","message":{"role":"assistant","content":[{"type":"text","text":"I'll start"}],"stopReason":"error","errorMessage":"This operation was aborted"}}
`)
	s := &Session{Dir: dir}
	if msgs := s.messages(); len(msgs) != 1 || s.HandedBack() {
		t.Fatalf("a failed attempt pi has not settled is not a stop: %+v", msgs)
	}
	appendRecord(t, path, `{"type":"context_edit","id":"c","parentId":"b","timestamp":"2026-11-12T00:11:54.100Z","targetId":"b","replacement":null}`)
	settled(path)
	if msgs := s.messages(); len(msgs) != 1 || msgs[0].Role != "you" {
		t.Fatalf("an attempt pi is retrying is not a bubble: %+v", msgs)
	}
	if s.HandedBack() {
		t.Fatal("while pi waits to retry, the turn is still working")
	}
	appendRecord(t, path, `{"type":"message","id":"d","parentId":"c","timestamp":"2026-11-12T00:12:20.000Z","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"This operation was aborted"}}`)
	settled(path)
	msgs := s.messages()
	if len(msgs) != 2 || !msgs[1].Error || msgs[1].I != 1 {
		t.Fatalf("the failed retry is the one error bubble: %+v", msgs)
	}
	if !strings.Contains(msgs[1].Text, "failed again when I retried") || strings.Contains(msgs[1].Text, "reach") {
		t.Fatalf("the bubble says the turn was retried, not that the model was unreachable: %q", msgs[1].Text)
	}
	if !s.HandedBack() {
		t.Fatal("a failed retry ends the turn")
	}
}

// Pressing stop while pi waits to retry cancels the retry, and pi writes
// nothing for it: the stop itself ends the turn, so the chat does not stay
// working; a message the person sends after the stop starts new work.
func TestStopWhileWaitingToRetryEndsTheTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeSessionRecord(t, dir, "2026-11-12T00-11-00-000Z_console.jsonl",
		`{"type":"message","id":"a","timestamp":"2026-11-12T00:11:00.000Z","message":{"role":"user","content":[{"type":"text","text":"add a login page"}]}}
{"type":"message","id":"b","parentId":"a","timestamp":"2026-11-12T00:11:54.000Z","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"503 overloaded"}}
{"type":"context_edit","id":"c","parentId":"b","timestamp":"2026-11-12T00:11:54.100Z","targetId":"b","replacement":null}
`)
	s := &Session{Dir: dir}
	if s.HandedBack() {
		t.Fatal("waiting to retry is working")
	}
	s.record().interrupted()
	if !s.HandedBack() {
		t.Fatal("a stop while pi waits to retry ends the turn")
	}
	appendRecord(t, path, `{"type":"message","id":"d","parentId":"c","timestamp":"2099-01-01T00:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"try again"}]}}`)
	appendRecord(t, path, `{"type":"message","id":"e","parentId":"d","timestamp":"2099-01-01T00:00:05.000Z","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"503 overloaded"}}`)
	appendRecord(t, path, `{"type":"context_edit","id":"f","parentId":"e","timestamp":"2099-01-01T00:00:05.100Z","targetId":"e","replacement":null}`)
	if s.HandedBack() {
		t.Fatal("the stop belongs to the turn it ended, not the next one's retry")
	}
}

func appendRecord(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// settled ages the record past the settle time.
func settled(path string) {
	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(path, past, past)
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

// A split install copies the worker record every two seconds. The stop
// can succeed on the worker before its retry omission reaches AppTown.
func TestStopBeforeRetryRecordCopyEndsTheTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeSessionRecord(t, dir, "2026-11-12T00-11-00-000Z_console.jsonl",
		`{"type":"message","id":"a","timestamp":"2026-11-12T00:11:00.000Z","message":{"role":"user","content":[{"type":"text","text":"add a login page"}]}}
`)
	s := &Session{Dir: dir}
	if s.HandedBack() {
		t.Fatal("the turn has started")
	}
	// Escape already cancelled pi's backoff on the worker. No further
	// messages will be written there. AppTown's copy still has only a.
	s.record().interrupted()
	appendRecord(t, path, `{"type":"message","id":"b","timestamp":"2026-11-12T00:11:54.000Z","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"503 overloaded"}}`)
	appendRecord(t, path, `{"type":"context_edit","id":"c","timestamp":"2026-11-12T00:11:54.100Z","targetId":"b","replacement":null}`)
	settled(path)
	if !s.HandedBack() {
		t.Fatal("stopped retry stays working after its delayed worker record arrives")
	}
}
