package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// transcriptAgent is one project's run with an agent step, admitted and
// driven through the owner's attempt and retry operations.
type transcriptAgent struct {
	db       *database.DB
	run      *Run
	step     ids.ID
	attempts stepAttempts
	retries  stepRetries
	dataDir  string
}

func transcriptFixture(t *testing.T) *transcriptAgent {
	t.Helper()
	q, run := admitWorkflowSpec(t, transcriptSpec())
	return transcriptAgentOf(t, q.db, run)
}

func transcriptSpec() schema.Spec {
	return schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "build", Uses: "agent/pi"}}}}}
}

// transcriptProjectIn admits a second project's run in the same database.
func transcriptProjectIn(t *testing.T, db *database.DB, slug string) *transcriptAgent {
	t.Helper()
	project := workflowTestProject(t, db, slug, slug, "")
	change := admissionTestChange(t, db, project, "another change")
	admissions := newRunAdmissions(db, NewStages(nil, func(kind string) bool { return kind == "agent/pi" }), nil)
	run, err := admissions.Create(context.Background(), StartRequest{ProjectID: project, ChangeID: change, WorkflowName: "test", InputsJSON: "{}", requestedBy: "operator"},
		PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 1}, Spec: transcriptSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	return transcriptAgentOf(t, db, run)
}

func transcriptAgentOf(t *testing.T, db *database.DB, run *Run) *transcriptAgent {
	return &transcriptAgent{
		db: db, run: run, step: admittedStepID(t, queries{db: db}, run, "build"),
		attempts: newStepAttempts(db, func(kind string) bool { return kind == "agent/pi" }),
		retries:  newStepRetries(db, []string{"agent/pi"}),
		dataDir:  t.TempDir(),
	}
}

func (f *transcriptAgent) ref() RunRef { return RunRef{ID: f.run.ID, ProjectID: f.run.ProjectID} }

// start claims the next attempt and returns it with the session its start
// receipt names.
func (f *transcriptAgent) start(t *testing.T) (*Step, string) {
	t.Helper()
	snapshot, err := readStep(context.Background(), f.db, f.step)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := f.attempts.Start(context.Background(), snapshot, "agent/pi")
	if err != nil {
		t.Fatal(err)
	}
	session, err := AgentSessionIn(context.Background(), f.db, f.ref(), f.step, RetryDefault)
	if err != nil {
		t.Fatal(err)
	}
	return claim, session.ID
}

func (f *transcriptAgent) finish(t *testing.T, claim *Step) {
	t.Helper()
	if err := f.attempts.Finish(context.Background(), claim, StepOutcome{Status: StatusFail, Evidence: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func (f *transcriptAgent) retryContinue(t *testing.T) {
	t.Helper()
	if _, err := f.retries.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: f.step, Mode: RetryContinue}, Actor: commandActor{Receipt: "person"}}, nil); err != nil {
		t.Fatal(err)
	}
}

// keep stores raw and records it for the attempt's invocation.
func (f *transcriptAgent) keep(t *testing.T, claim *Step, session string, invocation int64, raw []byte) string {
	t.Helper()
	if err := KeepTranscript(context.Background(), f.db, f.dataDir, TranscriptCapture{Run: f.ref(), StepID: f.step, Attempt: claim.Attempt, Session: session, Invocation: invocation, At: time.Now()}, raw, ""); err != nil {
		t.Fatal(err)
	}
	return digestOf(raw)
}

func (f *transcriptAgent) record(claim *Step, session string, invocation int64, digest, unavailable string) error {
	return recordTranscript(context.Background(), f.db, TranscriptCapture{Run: f.ref(), StepID: f.step, Attempt: claim.Attempt, Session: session, Invocation: invocation, Digest: digest, Unavailable: unavailable, At: time.Now()})
}

// readsBytes asserts the attempt's receipt names exactly want.
func (f *transcriptAgent) readsBytes(t *testing.T, attempt int64, want []byte) {
	t.Helper()
	receipt, err := StepTranscriptIn(context.Background(), f.db, f.ref(), f.step, attempt)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadTranscript(f.dataDir, receipt)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("attempt %d reads %q (%v), want %q", attempt, got, err, want)
	}
}

func transcriptOf(session string, turns ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"session","version":3,"id":%q,"timestamp":"2026-10-01T00:00:00.000Z","cwd":"/work"}`+"\n", session)
	for _, turn := range turns {
		fmt.Fprintf(&b, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n", turn)
	}
	return []byte(b.String())
}

// A continued attempt reuses its session id but records its own bytes; each
// continuation resumes the attempt before it, and every earlier receipt still
// reads its own bytes after later ones are recorded.
func TestContinuedAttemptsEachKeepTheirOwnTranscript(t *testing.T) {
	t.Parallel()
	f := transcriptFixture(t)
	first, session := f.start(t)
	one := transcriptOf(session, "first")
	d1 := f.keep(t, first, session, 1, one)
	f.finish(t, first)

	f.retryContinue(t)
	second, continued := f.start(t)
	if continued != session {
		t.Fatalf("a continue resumed %q, want %q", continued, session)
	}
	earlier, err := ContinuedTranscriptIn(context.Background(), f.db, f.ref(), f.step, second.Attempt, session)
	if err != nil || earlier.StepID != f.step || earlier.Attempt != first.Attempt || earlier.Digest != d1 {
		t.Fatalf("second attempt resumes %+v (%v), want attempt %d's %s", earlier, err, first.Attempt, d1)
	}
	two := transcriptOf(session, "first", "second")
	d2 := f.keep(t, second, session, 1, two)
	f.finish(t, second)

	f.retryContinue(t)
	third, _ := f.start(t)
	earlier, err = ContinuedTranscriptIn(context.Background(), f.db, f.ref(), f.step, third.Attempt, session)
	if err != nil || earlier.Attempt != second.Attempt || earlier.Digest != d2 {
		t.Fatalf("third attempt resumes %+v (%v), want attempt %d's %s", earlier, err, second.Attempt, d2)
	}
	f.readsBytes(t, first.Attempt, one)
	f.readsBytes(t, second.Attempt, two)
	// The third attempt recorded nothing yet: nothing it could resume.
	if receipt, err := StepTranscriptIn(context.Background(), f.db, f.ref(), f.step, third.Attempt); err != nil || receipt.Recorded {
		t.Fatalf("an attempt with no capture: %+v %v", receipt, err)
	}
	// Taking the lane to chat reads the producer's latest attempt's receipt.
	d3 := f.keep(t, third, session, 1, transcriptOf(session, "first", "second", "third"))
	service := newService(f.db, authorityFunc(func(context.Context, operationPermission) (string, error) { return "reader", nil }), Runtime{})
	chat, err := service.ChatContinuation(context.Background(), f.run.ChangeID, f.step)
	if err != nil || chat.History.Digest != d3 || chat.History.Attempt != third.Attempt || chat.History.Session != session || chat.History.StepID != f.step {
		t.Fatalf("the chat handoff's history: %+v %v", chat.History, err)
	}
}

// A crash after the file is stored and before its receipt is recorded
// leaves an unreferenced file and the older receipt reading its own bytes.
// The unreferenced file is removed once it is older than the bound, never
// before, and nothing else in the store is: referenced files, and the
// workspace reaper's folders of old session files.
func TestATranscriptStoredWithoutItsReceiptLeavesTheOlderReceiptIntact(t *testing.T) {
	t.Parallel()
	f := transcriptFixture(t)
	claim, session := f.start(t)
	one := transcriptOf(session, "first")
	f.keep(t, claim, session, 1, one)
	later := transcriptOf(session, "first", "wrap-up")
	orphan, err := StoreTranscript(f.dataDir, later)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(transcriptPath(f.dataDir, orphan)); err != nil {
		t.Fatalf("the stored file: %v", err)
	}
	f.readsBytes(t, claim.Attempt, one)
	reaped := filepath.Join(f.dataDir, "transcripts", "run-1")
	if err := os.MkdirAll(reaped, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reaped, "x_"+session+".jsonl"), one, 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := RemoveOrphanedTranscripts(context.Background(), f.db, f.dataDir, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("a file newer than the bound was removed: %d %v", n, err)
	}
	if n, err := RemoveOrphanedTranscripts(context.Background(), f.db, f.dataDir, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("removing the unreferenced file: %d %v", n, err)
	}
	if _, err := os.Stat(transcriptPath(f.dataDir, orphan)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unreferenced file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(reaped, "x_"+session+".jsonl")); err != nil {
		t.Fatalf("the reaper's folder: %v", err)
	}
	f.readsBytes(t, claim.Attempt, one)
}

// Storing bytes already kept renews their file's time, so the orphan sweep
// cannot remove it while its new receipt is being written.
func TestStoringKeptBytesAgainRenewsTheirTime(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	raw := transcriptOf("s", "turn")
	digest, err := StoreTranscript(dataDir, raw)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(transcriptPath(dataDir, digest), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreTranscript(dataDir, raw); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(transcriptPath(dataDir, digest)); err != nil || !info.ModTime().After(old.Add(time.Hour)) {
		t.Fatalf("the kept file's time was not renewed: %v %v", info.ModTime(), err)
	}
}

// A receipt update that fails after an earlier capture keeps the earlier
// capture: the attempt still reads the bytes it recorded first.
func TestAFailedReceiptUpdateKeepsThePreviousCapture(t *testing.T) {
	t.Parallel()
	f := transcriptFixture(t)
	claim, session := f.start(t)
	one := transcriptOf(session, "first")
	f.keep(t, claim, session, 1, one)
	d2, err := StoreTranscript(f.dataDir, transcriptOf(session, "first", "wrap-up"))
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, f.db, "-- name: RecordAgentTranscript")
	if err := f.record(claim, session, 2, d2, ""); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("the failed update: %v", err)
	}
	f.readsBytes(t, claim.Attempt, one)
}

// Each invocation of an attempt (a wrap-up) records its own row, and the
// attempt reads its latest; a replay of an older capture, or a second answer
// for the same invocation, never brings back other bytes. Every capture keeps
// its reference, so no stored file is left unreferenced.
func TestAReplayedOlderCaptureDoesNotReplaceANewerOne(t *testing.T) {
	t.Parallel()
	f := transcriptFixture(t)
	claim, session := f.start(t)
	one, two := transcriptOf(session, "first"), transcriptOf(session, "first", "wrap-up")
	d1 := f.keep(t, claim, session, 1, one)
	f.keep(t, claim, session, 2, two)
	f.readsBytes(t, claim.Attempt, two)
	if err := f.record(claim, session, 1, d1, ""); err != nil {
		t.Fatal(err)
	}
	f.readsBytes(t, claim.Attempt, two)
	if err := f.record(claim, session, 2, "", "a different answer"); err != nil {
		t.Fatal(err)
	}
	f.readsBytes(t, claim.Attempt, two)
	// A later invocation whose capture failed says so: the attempt's latest
	// history is not kept.
	if err := f.record(claim, session, 3, "", "no transcript names the session"); err != nil {
		t.Fatal(err)
	}
	receipt, err := StepTranscriptIn(context.Background(), f.db, f.ref(), f.step, claim.Attempt)
	var gone *HistoryUnavailable
	if _, readErr := ReadTranscript(f.dataDir, receipt); err != nil || !errors.As(readErr, &gone) || !strings.Contains(readErr.Error(), "no transcript names the session") {
		t.Fatalf("a failed later capture: %+v %v %v", receipt, err, readErr)
	}
	if n, err := RemoveOrphanedTranscripts(context.Background(), f.db, f.dataDir, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("an earlier invocation's capture lost its reference: %d removed, %v", n, err)
	}
}

// A capture is recorded only against the attempt whose start receipt names
// its session, and only as a digest or a reason.
func TestATranscriptIsRecordedOnlyForItsAttemptsSession(t *testing.T) {
	t.Parallel()
	f := transcriptFixture(t)
	claim, session := f.start(t)
	digest, err := StoreTranscript(f.dataDir, transcriptOf(session, "first"))
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"another session":      f.record(claim, "step-1-a9", 1, digest, ""),
		"an unstarted attempt": recordTranscript(context.Background(), f.db, TranscriptCapture{Run: f.ref(), StepID: f.step, Attempt: claim.Attempt + 1, Session: session, Invocation: 1, Digest: digest, At: time.Now()}),
		"another project":      recordTranscript(context.Background(), f.db, TranscriptCapture{Run: RunRef{ID: f.run.ID, ProjectID: idstest.N(999999)}, StepID: f.step, Attempt: claim.Attempt, Session: session, Invocation: 1, Digest: digest, At: time.Now()}),
		"both":                 f.record(claim, session, 1, digest, "and a reason"),
		"neither":              f.record(claim, session, 1, "", ""),
		"not a digest":         f.record(claim, session, 1, "abc", ""),
	} {
		if err == nil {
			t.Errorf("%s: recorded", name)
		}
	}
	if receipt, err := StepTranscriptIn(context.Background(), f.db, f.ref(), f.step, claim.Attempt); err != nil || receipt.Recorded {
		t.Fatalf("a refused capture was recorded: %+v %v", receipt, err)
	}
}

// History that was never kept, has gone, or whose bytes do not match their
// digest is unavailable, with why; it is never read as something else.
func TestUnkeptOrDamagedHistoryIsUnavailable(t *testing.T) {
	t.Parallel()
	for _, damage := range []string{"recorded unavailable", "missing file", "changed bytes", "not recorded"} {
		t.Run(damage, func(t *testing.T) {
			t.Parallel()
			f := transcriptFixture(t)
			claim, session := f.start(t)
			want := damage
			switch damage {
			case "recorded unavailable":
				want = "the transcript holds only its header"
				if err := f.record(claim, session, 1, "", want); err != nil {
					t.Fatal(err)
				}
			case "missing file":
				digest := f.keep(t, claim, session, 1, transcriptOf(session, "first"))
				if err := os.Remove(transcriptPath(f.dataDir, digest)); err != nil {
					t.Fatal(err)
				}
				want = "missing"
			case "changed bytes":
				digest := f.keep(t, claim, session, 1, transcriptOf(session, "first"))
				if err := os.WriteFile(transcriptPath(f.dataDir, digest), transcriptOf(session, "other"), 0o644); err != nil {
					t.Fatal(err)
				}
				want = "does not match"
			case "not recorded":
				want = "no history was recorded"
			}
			receipt, err := StepTranscriptIn(context.Background(), f.db, f.ref(), f.step, claim.Attempt)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := ReadTranscript(f.dataDir, receipt)
			var gone *HistoryUnavailable
			if raw != nil || !errors.As(err, &gone) || !strings.HasPrefix(err.Error(), "the earlier session's history is not available") || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: %q %v", damage, raw, err)
			}
		})
	}
}

// Kept bytes are named by their content: storing them again changes
// nothing, and a file whose bytes do not match its name is replaced.
func TestStoredTranscriptsAreNamedByTheirContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := transcriptOf("s", "first")
	digest, err := StoreTranscript(dir, raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := StoreTranscript(dir, raw)
	if err != nil || again != digest {
		t.Fatalf("stored again: %s %v", again, err)
	}
	if err := os.WriteFile(transcriptPath(dir, digest), []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreTranscript(dir, raw); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTranscript(dir, TranscriptReceipt{Recorded: true, Digest: digest})
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("repaired: %q %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "transcripts"))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

// A deleted project's transcript receipts go with its records, and its
// cleanup removes each kept file no remaining receipt names: one another
// project's receipt also names stays.
func TestProjectRemovalRemovesOnlyUnreferencedTranscripts(t *testing.T) {
	t.Parallel()
	gone := transcriptFixture(t)
	kept := transcriptProjectIn(t, gone.db, "kept")
	kept.dataDir = gone.dataDir
	claim, session := gone.start(t)
	first := gone.keep(t, claim, session, 1, transcriptOf(session, "an earlier invocation"))
	own := gone.keep(t, claim, session, 2, transcriptOf(session, "only here"))
	gone.finish(t, claim)
	shared := transcriptOf("shared", "the same bytes")
	sharedDigest, err := StoreTranscript(gone.dataDir, shared)
	if err != nil {
		t.Fatal(err)
	}
	// The same bytes kept by both projects. Real sessions' bytes differ;
	// the reference rule must not depend on that.
	gone.retryContinue(t)
	again, _ := gone.start(t)
	if err := gone.record(again, session, 1, sharedDigest, ""); err != nil {
		t.Fatal(err)
	}
	otherClaim, otherSession := kept.start(t)
	if err := kept.record(otherClaim, otherSession, 1, sharedDigest, ""); err != nil {
		t.Fatal(err)
	}
	transitionScopeRun(t, gone.db, workflowsql.TransitionRunParams{At: time.Now(), ID: gone.run.ID, NextStatus: RunDone.String()})
	var removal ProjectRemoval
	if err := gone.db.InTx(context.Background(), func(tx *database.Tx) error {
		var err error
		if removal, err = ProjectRemovalIn(context.Background(), tx, gone.run.ProjectID); err != nil {
			return err
		}
		return RemoveProjectIn(context.Background(), tx, gone.run.ProjectID)
	}); err != nil {
		t.Fatal(err)
	}
	if len(removal.Transcripts) != 3 {
		t.Fatalf("the cleanup plan names %v, want every kept transcript, each invocation's", removal.Transcripts)
	}
	if err := RemoveUnreferencedTranscripts(context.Background(), gone.db, gone.dataDir, removal.Transcripts); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{first, own} {
		if _, err := os.Stat(transcriptPath(gone.dataDir, d)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the deleted project's own transcript: %v", err)
		}
	}
	kept.readsBytes(t, otherClaim.Attempt, shared)
	if receipt, err := StepTranscriptIn(context.Background(), gone.db, gone.ref(), gone.step, claim.Attempt); err != nil || receipt.Recorded {
		t.Fatalf("the deleted project's receipt: %+v %v", receipt, err)
	}
	// Again, as a retried cleanup: already absent is done.
	if err := RemoveUnreferencedTranscripts(context.Background(), gone.db, gone.dataDir, removal.Transcripts); err != nil {
		t.Fatal(err)
	}
}

func digestOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Interpose exactly after the sweep's age check and before its reference query.
// This represents a concurrent recovery storing its capture in that interval.
type reviewS5RenewBeforeReferenceQuery struct {
	db    *database.DB
	renew func()
}

func (q reviewS5RenewBeforeReferenceQuery) SQL() database.Handle {
	q.renew()
	return q.db.SQL()
}

// A capture is
// stored and recorded by one operation (KeepTranscript), so the concurrent
// recovery is that operation, started in the interval between the sweep's age
// read and its reference query. It must not complete there — neither its
// store nor its receipt may land inside the sweep's observation — and once
// the sweep is done its receipt must name readable bytes, whether or not the
// sweep removed the old file first.
func TestReviewS5SweepPreservesAConcurrentlyRenewedCapture(t *testing.T) {
	f := transcriptFixture(t)
	claim, session := f.start(t)
	raw := transcriptOf(session, "a capture recovered after a crash")
	digest, err := StoreTranscript(f.dataDir, raw)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(transcriptPath(f.dataDir, digest), old, old); err != nil {
		t.Fatal(err)
	}
	captured := make(chan error, 1)
	q := reviewS5RenewBeforeReferenceQuery{db: f.db, renew: func() {
		go func() {
			captured <- KeepTranscript(context.Background(), f.db, f.dataDir, TranscriptCapture{Run: f.ref(), StepID: f.step, Attempt: claim.Attempt, Session: session, Invocation: 1, At: time.Now()}, raw, "")
		}()
		select {
		case err := <-captured:
			t.Errorf("a capture completed inside the sweep's age read and reference query (%v)", err)
			captured <- err
		case <-time.After(300 * time.Millisecond):
		}
	}}
	removed, err := RemoveOrphanedTranscripts(context.Background(), q, f.dataDir, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := <-captured; err != nil {
		t.Fatal(err)
	}
	t.Logf("sweep removed %d captures", removed)
	f.readsBytes(t, claim.Attempt, raw)
}
