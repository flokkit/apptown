package pi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"flokkit.io/forge/internal/durable"
)

// MaxTranscriptBytes bounds one step session's transcript AppTown keeps.
// It matches the largest input a worker task accepts, so a kept transcript
// can always be sent back to continue its session.
const MaxTranscriptBytes = 64 << 20

// Pi finds a session's file again by the id in its first line, the session
// header, not by the file's name (design/operations/workers.md). The header
// is at most a few hundred
// bytes; a longer first line is not a header.
const maxHeaderBytes = 64 << 10

type sessionHeader struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// headerSession returns the session id the first line of raw names, or ""
// when that line is not Pi's session header.
func headerSession(first []byte) string {
	var h sessionHeader
	if err := json.Unmarshal(bytes.TrimSpace(first), &h); err != nil || h.Type != "session" {
		return ""
	}
	return h.ID
}

// CheckTranscript says why raw cannot be kept as session's history, or ""
// when it can: its first line is Pi's session header naming session, and it
// holds more than that header (a session that never took a turn has nothing
// to continue).
func CheckTranscript(raw []byte, session string) string {
	switch {
	case len(raw) == 0:
		return "the transcript is empty"
	case len(raw) > MaxTranscriptBytes:
		return fmt.Sprintf("the transcript is larger than %d MiB", MaxTranscriptBytes>>20)
	}
	first, rest, _ := bytes.Cut(raw, []byte("\n"))
	got := headerSession(first)
	switch {
	case got == "":
		return "the transcript does not start with Pi's session header"
	case got != session:
		return fmt.Sprintf("the transcript's header names session %q, not %q", got, session)
	case len(bytes.TrimSpace(rest)) == 0:
		return "the transcript holds only its header"
	}
	return ""
}

// StepTranscript reads the one transcript in dir whose header names session
// and checks it (CheckTranscript). Never the newest file: a second file
// naming the same session is ambiguous and refused. Only regular files are
// read, never through a link: the agent writes this directory. reason is
// empty when raw can be kept.
func StepTranscript(dir, session string) (raw []byte, reason string) {
	files, reason := sessionFiles(dir, session)
	switch {
	case reason != "":
		return nil, reason
	case len(files) == 0:
		return nil, fmt.Sprintf("no transcript names session %q", session)
	case len(files) > 1:
		return nil, fmt.Sprintf("%d transcripts name session %q", len(files), session)
	}
	raw, err := readRegular(files[0], MaxTranscriptBytes)
	if err != nil {
		return nil, "reading the transcript: " + err.Error()
	}
	if reason := CheckTranscript(raw, session); reason != "" {
		return nil, reason
	}
	return raw, ""
}

// ResumeTranscript makes raw, a session's kept transcript, the one file in
// dir that Pi resumes for session: it replaces the file there that names the
// session, or creates one named as Pi names its files. Pi then appends to
// exactly the bytes AppTown kept. A directory holding two files that name
// the session is refused: which one Pi would resume is not known.
func ResumeTranscript(dir, session string, raw []byte, now time.Time) error {
	if reason := CheckTranscript(raw, session); reason != "" {
		return fmt.Errorf("the kept transcript cannot be resumed: %s", reason)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files, reason := sessionFiles(dir, session)
	if reason != "" {
		return fmt.Errorf("%s", reason)
	}
	if len(files) > 1 {
		return fmt.Errorf("%d transcripts in the folder name session %q", len(files), session)
	}
	target := filepath.Join(dir, now.UTC().Format("2006-01-02T15-04-05-000Z")+"_"+session+".jsonl")
	if len(files) == 1 {
		target = files[0]
		if current, err := readRegular(target, MaxTranscriptBytes); err == nil && bytes.Equal(current, raw) {
			return nil // already the kept bytes: the file Pi wrote stays as it is
		}
	}
	// A rename replaces a link planted at the name, never what it points to.
	_, err := durable.Write(filepath.Dir(target), filepath.Base(target), raw, 0o644, false)
	return err
}

// sessionFiles lists the regular .jsonl files in dir whose header names
// session, in name order. A directory that is itself a link is refused.
func sessionFiles(dir, session string) ([]string, string) {
	if session == "" || strings.ContainsAny(session, "/\\") {
		return nil, fmt.Sprintf("session %q cannot name a transcript", session)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if base := filepath.Base(d); base != ".apptown" && base != "sessions" {
			break
		}
		if fi, err := os.Lstat(d); os.IsNotExist(err) {
			return nil, ""
		} else if err != nil {
			return nil, "reading the session directory: " + err.Error()
		} else if !fi.IsDir() {
			return nil, fmt.Sprintf("%s is not a directory", filepath.Base(d))
		}
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, ""
	}
	if err != nil {
		return nil, "reading the session directory: " + err.Error()
	}
	var out []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		first, err := firstLine(path)
		if err != nil {
			continue
		}
		if headerSession(first) == session {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out, ""
}

func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	return f, nil
}

func firstLine(path string) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(io.LimitReader(f, maxHeaderBytes), maxHeaderBytes).ReadSlice('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	return line, nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// One byte over the limit is enough for CheckTranscript to refuse it.
	return io.ReadAll(io.LimitReader(f, limit+1))
}
