package workflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/durable"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// An agent attempt's session history is kept by the forge, not only in a
// folder (design/operations/workers.md, "Agent and review steps on the
// worker"). Each captured transcript is an immutable
// file named by its digest under dataDir/transcripts, written and synced
// before the attempt's receipt records the digest. Each invocation records
// its own row, and the attempt's history is its latest invocation's. A
// continued attempt reuses its session id but records its own digest, so an
// earlier receipt still names its own bytes; a crash between the file and
// the receipt leaves an unreferenced file, which the forge removes a day
// later (RemoveOrphanedTranscripts).
//
// Storing a capture and recording its receipt is one operation
// (KeepTranscript), and deleting a file no receipt names is excluded from it
// for the same store: both hold transcriptStore. Otherwise a sweep could read
// a file's old time and find no receipt, a capture renew the file and record
// its receipt, and the sweep then delete bytes a receipt names. An in-process
// lock is enough because the forge is one process:
// every capture, the startup backfill, the orphan sweep and project cleanup
// run inside it against its own data directory. A second process sharing
// that directory would need a lock on the directory instead.

// maxKeptTranscript bounds what a read loads; the capture refuses larger.
const maxKeptTranscript = 64 << 20

// TranscriptReceipt is what an attempt's receipt says about its session's
// history. A zero StepID means no attempt holds the session. Recorded is false
// for an attempt that kept nothing (one the forge lost mid-invocation): it
// has no history to continue. Runs from before transcripts were kept had
// their folder's file imported at startup (UnrecordedSessionsIn). A recorded
// attempt has either a Digest or the reason it has none.
type TranscriptReceipt struct {
	StepID      ids.ID
	Attempt     int64
	Session     string
	Recorded    bool
	Digest      string
	Unavailable string
}

// HistoryUnavailable says a session's recorded history cannot be given back.
// A continuation of it is refused; it never starts a fresh session instead.
type HistoryUnavailable struct {
	Session, Reason string
}

func (e *HistoryUnavailable) Error() string {
	return "the earlier session's history is not available: " + e.Reason
}

// transcriptStore excludes deletion from a capture's store-and-record.
var transcriptStore sync.Mutex

func transcriptPath(dataDir, digest string) string {
	return filepath.Join(dataDir, "transcripts", digest+".jsonl")
}

func validDigest(d string) bool {
	if len(d) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(d)
	return err == nil
}

// StoreTranscript keeps raw as dataDir/transcripts/<sha256>.jsonl and returns
// the digest, written with durable.Write. A file already there with those bytes is done, its time renewed;
// one whose bytes do not match its name is replaced. Alone it is not
// excluded from the orphan sweep: bytes a receipt will name are kept with
// KeepTranscript.
func StoreTranscript(dataDir string, raw []byte) (string, error) {
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	dir := filepath.Join(dataDir, "transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	final := transcriptPath(dataDir, digest)
	if _, err := readTranscriptFile(final, digest); err == nil {
		// Its modification time is what the orphan sweep judges, on the
		// system clock (clock.System).
		now := clock.System()
		if err := os.Chtimes(final, now, now); err != nil {
			return "", err
		}
		return digest, nil
	}
	if _, err := durable.Write(dir, filepath.Base(final), raw, 0o600, false); err != nil {
		return "", err
	}
	return digest, nil
}

// ReadTranscript returns the bytes r names, checked against its digest. A
// receipt that recorded no history, a missing file or bytes that do not
// match are a *HistoryUnavailable.
func ReadTranscript(dataDir string, r TranscriptReceipt) ([]byte, error) {
	unavailable := func(reason string) error { return &HistoryUnavailable{Session: r.Session, Reason: reason} }
	switch {
	case !r.Recorded:
		return nil, unavailable("no history was recorded for it")
	case r.Unavailable != "":
		return nil, unavailable(r.Unavailable)
	case !validDigest(r.Digest):
		return nil, unavailable(fmt.Sprintf("its recorded digest %q is not a transcript's", r.Digest))
	}
	raw, err := readTranscriptFile(transcriptPath(dataDir, r.Digest), r.Digest)
	if err != nil {
		return nil, unavailable(err.Error())
	}
	return raw, nil
}

func readTranscriptFile(path, digest string) ([]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("its kept transcript is missing")
	}
	if err != nil {
		return nil, fmt.Errorf("reading its kept transcript: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeptTranscript+1))
	if err != nil {
		return nil, fmt.Errorf("reading its kept transcript: %w", err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("its kept transcript does not match its recorded digest")
	}
	return raw, nil
}

// TranscriptCapture is one invocation's captured history for an attempt:
// the digest StoreTranscript returned, or why nothing was kept. Invocations
// of an attempt are numbered from 1 in the order they ran.
type TranscriptCapture struct {
	Run         RunRef
	StepID      ids.ID
	Attempt     int64
	Session     string
	Invocation  int64
	Digest      string
	Unavailable string
	At          time.Time // the forge's time it was recorded
}

// RecordTranscript records a capture with the attempt's session receipt,
// after its bytes are stored. The attempt's start receipt must name the
// session. Each invocation's capture is its own record, written once: a
// replay changes nothing. The attempt's history is its latest invocation's.
func RecordTranscript(ctx context.Context, db *database.DB, c TranscriptCapture) error {
	if c.Run.ID.IsZero() || c.Run.ProjectID.IsZero() || c.StepID.IsZero() || c.Attempt <= 0 || c.Invocation <= 0 || c.Session == "" || c.At.IsZero() {
		return fmt.Errorf("a transcript record needs its run, step, attempt, invocation and session")
	}
	if (c.Digest == "") == (c.Unavailable == "") {
		return fmt.Errorf("a transcript record needs a digest or the reason it has none, not both")
	}
	if c.Digest != "" && !validDigest(c.Digest) {
		return fmt.Errorf("transcript digest %q is not a sha256", c.Digest)
	}
	return db.InTx(ctx, func(tx *database.Tx) error {
		q := workflowsql.New(tx.SQL())
		started, err := q.AttemptSessionStarted(ctx, workflowsql.AttemptSessionStartedParams{StepID: c.StepID, RunID: c.Run.ID, ProjectID: c.Run.ProjectID, Attempt: c.Attempt, SessionID: c.Session})
		if err != nil {
			return err
		}
		if !started {
			return fmt.Errorf("step %d attempt %d did not start session %q", c.StepID, c.Attempt, c.Session)
		}
		_, err = q.RecordAgentTranscript(ctx, workflowsql.RecordAgentTranscriptParams{RunID: c.Run.ID, StepID: c.StepID, Attempt: c.Attempt, SessionID: c.Session, Invocation: c.Invocation, Digest: c.Digest, Unavailable: c.Unavailable, RecordedAt: c.At})
		return err
	})
}

// KeepTranscript keeps one invocation's history: raw stored by digest
// (StoreTranscript), then the capture recorded with the attempt's receipt
// (RecordTranscript), with no orphan removal in between. History that cannot
// be kept (reason, or a failed store) is recorded as unavailable, with why;
// only the record's failure is returned. Calling it again after a failed
// record stores the bytes again, so a retry never records a digest whose
// file is gone.
func KeepTranscript(ctx context.Context, db *database.DB, dataDir string, c TranscriptCapture, raw []byte, reason string) error {
	transcriptStore.Lock()
	defer transcriptStore.Unlock()
	c.Digest, c.Unavailable = "", ""
	if reason == "" {
		digest, err := StoreTranscript(dataDir, raw)
		if err != nil {
			reason = "the transcript could not be stored: " + err.Error()
		}
		c.Digest = digest
	}
	if reason != "" {
		c.Digest, c.Unavailable = "", reason
	}
	return RecordTranscript(ctx, db, c)
}

// StepTranscriptIn is what an attempt of a step kept of its session's
// history, with the session its start receipt names; attempt 0 is the
// step's latest attempt. A zero StepID means no such attempt started.
func StepTranscriptIn(ctx context.Context, q database.Queryer, ref RunRef, stepID ids.ID, attempt int64) (TranscriptReceipt, error) {
	row, err := workflowsql.New(q.SQL()).StepTranscript(ctx, workflowsql.StepTranscriptParams{StepID: stepID, RunID: ref.ID, ProjectID: ref.ProjectID, Attempt: attempt})
	if errors.Is(err, sql.ErrNoRows) {
		return TranscriptReceipt{}, nil
	}
	if err != nil {
		return TranscriptReceipt{}, err
	}
	return TranscriptReceipt{StepID: stepID, Attempt: row.Attempt, Session: row.SessionID, Recorded: row.Recorded, Digest: row.Digest, Unavailable: row.Unavailable}, nil
}

// ContinuedTranscriptIn names the history an attempt continuing session
// resumes: the latest other attempt in the project whose start receipt names
// the session (an earlier attempt of the step, or the source step of a
// continuation run). A zero StepID means none does.
func ContinuedTranscriptIn(ctx context.Context, q database.Queryer, ref RunRef, stepID ids.ID, attempt int64, session string) (TranscriptReceipt, error) {
	if ref.ProjectID.IsZero() || session == "" {
		return TranscriptReceipt{}, fmt.Errorf("a continued session needs its project and session")
	}
	row, err := workflowsql.New(q.SQL()).EarlierSessionTranscript(ctx, workflowsql.EarlierSessionTranscriptParams{ProjectID: ref.ProjectID, SessionID: session, StepID: stepID, Attempt: attempt})
	if errors.Is(err, sql.ErrNoRows) {
		return TranscriptReceipt{}, nil
	}
	if err != nil {
		return TranscriptReceipt{}, err
	}
	return TranscriptReceipt{StepID: row.StepID, Attempt: row.Attempt, Session: session, Recorded: row.Recorded, Digest: row.Digest, Unavailable: row.Unavailable}, nil
}

// RemoveUnreferencedTranscripts removes the kept transcript files among
// digests that no receipt references any more: a deleted project's, named in
// its cleanup plan. Files are shared by content, so a digest another receipt
// still names stays. An absent file is already done. Each digest's reference
// query and removal exclude captures (transcriptStore).
func RemoveUnreferencedTranscripts(ctx context.Context, q database.Queryer, dataDir string, digests []string) error {
	var failed []error
	for _, d := range digests {
		if !validDigest(d) {
			failed = append(failed, fmt.Errorf("transcript digest %q is not a sha256", d))
			continue
		}
		if _, err := removeUnreferenced(ctx, q, dataDir, d, time.Time{}); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

// removeUnreferenced removes digest's file when no receipt names it and,
// for a non-zero before, it was last written before then. The age read, the
// reference query and the removal happen under transcriptStore, so no
// capture stores or records between them. It reports whether it removed.
func removeUnreferenced(ctx context.Context, q database.Queryer, dataDir, digest string, before time.Time) (bool, error) {
	transcriptStore.Lock()
	defer transcriptStore.Unlock()
	file := transcriptPath(dataDir, digest)
	if !before.IsZero() {
		info, err := os.Lstat(file)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(before) {
			return false, nil
		}
	}
	referenced, err := workflowsql.New(q.SQL()).TranscriptReferenced(ctx, digest)
	if err != nil || referenced {
		return false, err
	}
	if err := os.Remove(file); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// UnrecordedSession is an agent attempt whose start receipt names a session
// but that recorded no history (UnrecordedSessionsIn).
type UnrecordedSession struct {
	Run     RunRef
	StepID  ids.ID
	Attempt int64
	Session string
	// Superseded: a later attempt in the project continued the session, so
	// a folder's file holds that attempt's history, not this one's.
	Superseded bool
}

// UnrecordedSessionsIn lists, oldest first and at most max, the agent
// attempts that recorded no history: runs from before transcripts were
// kept, and attempts the forge lost mid-invocation. An attempt still
// running is not listed; it records its own.
func UnrecordedSessionsIn(ctx context.Context, q database.Queryer, max int) ([]UnrecordedSession, error) {
	rows, err := workflowsql.New(q.SQL()).UnrecordedSessionStarts(ctx, int32(max))
	if err != nil {
		return nil, err
	}
	out := make([]UnrecordedSession, len(rows))
	for i, r := range rows {
		out[i] = UnrecordedSession{Run: RunRef{ID: r.RunID, ProjectID: r.ProjectID}, StepID: r.StepID, Attempt: r.Attempt, Session: r.SessionID, Superseded: r.Superseded}
	}
	return out, nil
}

// RemoveOrphanedTranscripts removes the kept transcript files last written
// before the time given that no receipt references: bytes stored before a
// crash kept their receipt from being written. The age bound is a grace
// period, not the exclusion: each file's age read, reference query and
// removal hold transcriptStore, so a capture storing and recording the same
// bytes runs wholly before or after them (KeepTranscript). Only digest-named
// files at the store's top level are considered; nothing else there is
// touched. It returns how many it removed.
func RemoveOrphanedTranscripts(ctx context.Context, q database.Queryer, dataDir string, before time.Time) (int, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "transcripts"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var failed []error
	for _, e := range entries {
		digest, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok || !validDigest(digest) || !e.Type().IsRegular() {
			continue
		}
		gone, err := removeUnreferenced(ctx, q, dataDir, digest, before)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if gone {
			removed++
		}
	}
	return removed, errors.Join(failed...)
}
