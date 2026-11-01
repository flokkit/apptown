package console

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const record = `{"type":"session","id":"s1","timestamp":"2026-05-15T09:00:00.000Z"}
{"type":"message","timestamp":"2026-05-15T09:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"make it blue"}]}}
{"type":"message","timestamp":"2026-05-15T09:00:02.000Z","message":{"role":"assistant","provider":"openrouter","model":"m","usage":{"input":10,"output":5,"totalTokens":15,"cost":{"total":0.001}},"content":[{"type":"thinking","text":"hmm"},{"type":"toolCall","name":"edit","arguments":{"path":"app.css"}}]}}
{"type":"message","timestamp":"2026-05-15T09:00:03.000Z","message":{"role":"toolResult","content":"ok"}}
{"type":"message","timestamp":"2026-05-15T09:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Done, it is blue."}]}}
not json
`

func writeRecord(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "sessions", "2026-05-15T09-00-00.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

// One parser: entries carry role, time, text, tools, usage and the stop
// reason; a corrupt line is skipped; a partial trailing line waits.
func TestReaderParsesAndResumes(t *testing.T) {
	_, path := writeRecord(t, record)
	rd := Reader{Path: path}
	entries, err := rd.Next()
	if err != nil || len(entries) != 5 {
		t.Fatalf("entries: %v %d", err, len(entries))
	}
	if entries[1].Role != "user" || entries[1].Text != "make it blue" || entries[1].At.IsZero() {
		t.Fatalf("user entry: %+v", entries[1])
	}
	a := entries[2]
	if a.Role != "assistant" || a.hasText() || len(a.Tools) != 1 || a.Tools[0].detail() != "app.css" || a.Usage.Total != 15 || a.Usage.Cost.Total != 0.001 {
		t.Fatalf("assistant entry: %+v", a)
	}
	off := rd.Off
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"message","timestamp":"2026-05-15T09:00:05.000Z","message":{"role":"user","content":"thanks"}}` + "\n" + `{"type":"message","message":{"role":"us`)
	_ = f.Close()
	more, err := rd.Next()
	if err != nil || len(more) != 1 || more[0].Text != "thanks" || rd.Off <= off {
		t.Fatalf("resume: %v %+v off %d>%d", err, more, rd.Off, off)
	}
	if e, ok := parseLine(`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"{\"error\":{\"message\":\"no credit\"}}"}}`); !ok || !e.failed() || e.aborted() {
		t.Fatalf("failed entry: %+v %v", e, ok)
	}
	if _, ok := parseLine("not json"); ok {
		t.Fatal("a corrupt line is skipped")
	}
	if !strings.Contains(helperErrorText(`x {"error":{"message":"no credit"}}`), "no credit") || !strings.Contains(helperErrorText(""), "the model call failed") {
		t.Fatal("helper error text")
	}
}

// The transcript is the record as bubbles: thinking and tool results stay
// under the hood, tools show their one detail, a failed turn is said as
// the helper, the hand-back is structural, and the submit marker sees a
// new user message.
func TestTranscript(t *testing.T) {
	dir, path := writeRecord(t, record)
	tr := &Transcript{Dir: dir}
	msgs := tr.messages()
	if len(msgs) != 3 || msgs[0].Role != "you" || msgs[1].Role != "helper" || msgs[1].Tools[0].Detail != "app.css" || msgs[2].Text != "Done, it is blue." || msgs[2].I != 2 {
		t.Fatalf("bubbles: %+v", msgs)
	}
	if tr.TurnHandedBack() {
		t.Fatal("a record written just now is not quiet yet")
	}
	if got := (&Session{Dir: dir}).Activity(); got != working {
		t.Fatalf("a chat whose record is still moving is working, got %q", got)
	}
	if got := (&Session{Dir: t.TempDir()}).Activity(); got != "" {
		t.Fatalf("a chat nobody has spoken in is not working, got %q", got)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if !tr.TurnHandedBack() {
		t.Fatal("a quiet record ending in an answer is handed back")
	}
	if got := (&Session{Dir: dir}).Activity(); got != HandedBack {
		t.Fatalf("a quiet chat ending in an answer has handed back, got %q", got)
	}
	file, size := tr.LatestSessionFile()
	if file != path || size == 0 {
		t.Fatalf("latest: %s %d", file, size)
	}
	if tr.UserMessageAppended(file, size) {
		t.Fatal("nothing appended yet")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"message","timestamp":"2026-05-15T09:00:05.000Z","message":{"role":"user","content":"thanks"}}` + "\n")
	_ = f.Close()
	if !tr.UserMessageAppended(file, size) {
		t.Fatal("the appended user message is seen")
	}
	before := tr.rd.Off
	if msgs = tr.messages(); len(msgs) != 4 || msgs[3].Text != "thanks" || tr.rd.Off <= before {
		t.Fatalf("resume: %+v", msgs)
	}
	// A chat that continues a lane's session hides the turns before it.
	later := &Transcript{Dir: dir, since: time.Date(2026, 5, 15, 9, 0, 4, 500_000_000, time.UTC)}
	if msgs := later.messages(); len(msgs) != 1 || msgs[0].Text != "thanks" {
		t.Fatalf("since: %+v", msgs)
	}
	// A stopped turn is said as the helper.
	_, path = writeRecord(t, `{"type":"message","timestamp":"2026-05-15T09:00:04.000Z","message":{"role":"assistant","stopReason":"aborted"}}`+"\n")
	stopped := &Transcript{Dir: filepath.Dir(filepath.Dir(path))}
	if msgs := stopped.messages(); len(msgs) != 1 || !msgs[0].Stopped || msgs[0].Role != "helper" {
		t.Fatalf("stopped: %+v", msgs)
	}
}

// The agent writes its record, so the reader reads only its own regular
// files: a link to a file elsewhere, or to a device, is not read and does
// not count as the conversation; a line past the bound is skipped without
// being held, and the lines around it are read.
func TestTheRecordReaderReadsOnlyTheChatsOwnFiles(t *testing.T) {
	dir, _ := writeRecord(t, record)
	outside := filepath.Join(t.TempDir(), "worker-file")
	if err := os.WriteFile(outside, []byte(`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"outside"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"2026-05-16T00-00-00.jsonl": outside, "2026-05-17T00-00-00.jsonl": os.DevNull} {
		if err := os.Symlink(target, filepath.Join(dir, "sessions", name)); err != nil {
			t.Fatal(err)
		}
	}
	tr := &Transcript{Dir: dir}
	files, err := tr.sessionFiles()
	if err != nil || len(files) != 1 || !strings.HasSuffix(files[0], "2026-05-15T09-00-00.jsonl") {
		t.Fatalf("record files: %v", files)
	}
	msgs := tr.messages()
	if len(msgs) == 0 || strings.Contains(msgs[len(msgs)-1].Text, "outside") {
		t.Fatalf("messages: %+v", msgs)
	}
	if _, err := (&Reader{Path: filepath.Join(dir, "sessions", "2026-05-16T00-00-00.jsonl")}).Next(); err == nil {
		t.Fatal("the reader followed a link out of the record's directory")
	}
	if _, err := (&Reader{Path: filepath.Join(dir, "sessions", "2026-05-17T00-00-00.jsonl")}).Next(); err == nil {
		t.Fatal("the reader read a device")
	}

	long := `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("x", maxLine) + `"}]}}` + "\n"
	_, path := writeRecord(t, record+long+`{"type":"message","timestamp":"2026-05-15T09:00:05.000Z","message":{"role":"user","content":[{"type":"text","text":"after"}]}}`+"\n")
	rd := Reader{Path: path}
	entries, err := rd.Next()
	if err != nil || len(entries) != 6 || entries[5].Text != "after" {
		t.Fatalf("entries around an overlong line: %d %v", len(entries), err)
	}
	if fi, _ := os.Stat(path); rd.Off != fi.Size() {
		t.Fatalf("offset %d after reading %d bytes", rd.Off, fi.Size())
	}
}

// A line past the bound is not held in memory while it is skipped.
func TestAnOverlongRecordLineIsNotHeld(t *testing.T) {
	defer func(old int) { maxLine = old }(maxLine)
	maxLine = 1 << 10
	_, path := writeRecord(t, `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"`+strings.Repeat("x", 8<<20)+`"}]}}`+"\n")
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rd := Reader{Path: path}
	entries, err := rd.Next()
	runtime.ReadMemStats(&after)
	if err != nil || len(entries) != 0 {
		t.Fatalf("an overlong line: %d entries, %v", len(entries), err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 2<<20 {
		t.Fatalf("reading past an 8 MiB line allocated %d bytes", grew)
	}
}

// Concurrent opens of one change share its chat, so two requests read one
// Session's record at once (TestConcurrentOpensOfAChangeShareOneChat raced
// here under -race in CI). Run with -race: reading must not write shared
// fields.
func TestOneSessionsRecordIsReadConcurrently(t *testing.T) {
	t.Parallel()
	s := &Session{Meta: Meta{AgentSession: "lane-agent"}, Dir: t.TempDir()}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Activity()
			_, _ = s.Records()
		}()
	}
	wg.Wait()
}

// A record folder that cannot be read is an error, never "no records": usage
// accounting reads this list, and a chat's records are removed after it.
// A folder that does not exist holds none.
func TestAnUnreadableRecordFolderIsNotNoRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if files, err := (&Transcript{Dir: dir}).sessionFiles(); err != nil || len(files) != 0 {
		t.Fatalf("no folder: %v %v", files, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions"), []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if files, err := (&Transcript{Dir: dir}).sessionFiles(); err == nil {
		t.Fatalf("an unreadable folder listed %v", files)
	}
}
