package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
)

// The container's view of a session, and the names pi and tmux are pinned
// to. Every path the steering promises and the runtime writes comes from
// here, so a mount cannot move without the prose and the code moving too.
const (
	WorkMount     = "/work"                 // the checkout (a change's worktree for a change console)
	Mount         = "/console"              // the session dir: pi-home, sessions, scratch, steering.md
	SessionsMount = Mount + "/sessions"     // pi --session-dir
	UploadsMount  = Mount + "/uploads"      // pasted pictures
	ScratchMount  = Mount + "/scratch"      // throwaway notes and experiments
	TmuxSession   = "console"               // tmux -s / -t
	AgentSession  = "console"               // the agent's session id, unless another agent's session is resumed
	MetaFile      = "session.json"          // present = alive
	EndedFile     = "ended.json"            // the record after End: skipped by recovery, read by the transcript
	SteeringFile  = "steering.md"           // the prompt the agent reads: authored text, the provision's notes, the notices
	AuthoredFile  = "steering.authored.md"  // the authored part, as handed to Open
	NoticesFile   = "notices.md"            // what the console told the agent since: resumes
	StagedFile    = "staged.json"           // the files the last provision placed, for replacement
	RehearsedFile = "rehearsed-fingerprint" // the change console's boot-rehearsal memory
	SavedFile     = "saved.json"            // the opener's record of the chat's last save to its branch
)

// Meta is the session's record on disk: the durable identity, enough to
// rebuild the session after a restart. It is the ONLY shape written to disk
// and read back; runtime state lives on Session and in the agent runtime.
// The console knows nothing of what a session is for: Scope and Labels are
// the opener's, kept and handed back.
type Meta struct {
	ID string `json:"id"`
	// Scope is what the opener groups sessions by ("project:56",
	// "change:305"); List, Transcript and Identity answer in its terms.
	Scope string `json:"scope"`
	// Labels are the opener's facts about the session, kept on the record
	// and returned with its identity. The console reads none of them.
	Labels map[string]string `json:"labels,omitempty"`
	Model  string            `json:"model"`
	// ModelPinned: the person named this model when opening the chat. An
	// unpinned session follows whatever the environment answers at wake.
	ModelPinned bool      `json:"modelPinned,omitempty"`
	Actor       string    `json:"actor"`
	CreatedAt   time.Time `json:"createdAt"`
	// AgentSession is the agent's own session id the runtime resumes
	// ("console" by default). Resuming another agent's session sets it to
	// that agent's, so the chat continues the agent that did the work.
	AgentSession string `json:"agentSession,omitempty"`
	// ResumedFrom: plain words for the header when another agent's session
	// was resumed.
	ResumedFrom string `json:"resumedFrom,omitempty"`
	// Token is the one credential the agent inside holds: it scopes what
	// the agent may do to its own session. It lives in the container env
	// and this record, never in a UI.
	Token string `json:"token"`
	// Checkout is the host path mounted at WorkMount. OwnsCheckout says the
	// console made it and deletes it with the session; otherwise it is
	// borrowed (a worktree) and stays. Writable says how it is mounted.
	Checkout     string `json:"checkout,omitempty"`
	OwnsCheckout bool   `json:"ownsCheckout,omitempty"`
	Writable     bool   `json:"writable,omitempty"`
	// Docker: the agent gets a private Docker daemon for its own builds.
	Docker bool `json:"docker,omitempty"`
	// LastUsed is the activity the server last saw when the record was
	// written; persisted so a restart does not age an active conversation
	// back to its birth.
	LastUsed time.Time `json:"lastUsed,omitempty"`
}

// Label is one of the opener's facts, "" when absent.
func (m Meta) Label(key string) string { return m.Labels[key] }

// Match selects sessions: by scope, and by labels that must all match.
type Match struct {
	Scope  string
	Labels map[string]string
}

func (m Match) matches(meta Meta) bool {
	if m.Scope != "" && meta.Scope != m.Scope {
		return false
	}
	for k, v := range m.Labels {
		if meta.Labels[k] != v {
			return false
		}
	}
	return true
}

// Workspace is the checkout the agent works in.
type Workspace struct {
	Path     string
	Writable bool
	Owned    bool // the console made it and deletes it with the session
}

// State is where a session stands on the ladder attached → detached →
// stopped → dead.
type State string

const (
	Attached State = "attached" // a terminal is open on it
	Detached State = "detached" // the agent runs, nobody is watching
	Stopped  State = "stopped"  // the agent is stopped; files intact; a message wakes it
	Dead     State = "dead"     // the agent runtime is gone; the next message rebuilds it
	// Unavailable: the runtime could not be asked (a worker that cannot be
	// reached); what runs is unknown, never assumed gone.
	Unavailable State = "unavailable"
)

// Turn is whether the agent is mid-turn or has handed back.
type Turn string

const (
	Working    Turn = "working"
	HandedBack Turn = "handed-back"
)

// Session is one conversation: its record, its directory on disk, and the
// runtime facts the service keeps (activity, viewers). The agent's process
// and terminal belong to the Agent runtime.
type Session struct {
	Meta
	Dir string // <data>/console/<id>

	mu       sync.Mutex
	lastUsed time.Time
	now      func() time.Time // the console's clock; nil is the real one
	viewers  int
	rec      Transcript
	recOnce  sync.Once // sets rec's Dir and Since: fixed for the session's life
	done     chan struct{}
	stopOnce sync.Once

	// Guarded by Service.mu: wakes admitted and not yet finished, and an
	// end in progress, which admits no further wake.
	waking int
	ending bool
}

func newSession(meta Meta, dir string, now func() time.Time) *Session {
	s := &Session{Meta: meta, Dir: dir, lastUsed: meta.LastUsed, done: make(chan struct{}), now: now}
	if s.lastUsed.IsZero() {
		s.lastUsed = s.clock()
	}
	return s
}

// clock is the time on the console's clock (Config.Now); a session built
// without one, a record read for its messages, reads the real clock.
func (s *Session) clock() time.Time { return clock.Or(s.now)() }

// Workspace is the checkout the agent works in.
func (s *Session) Workspace() Workspace {
	return Workspace{Path: s.Checkout, Writable: s.Meta.Writable, Owned: s.OwnsCheckout}
}

// Done is closed when the session ends; loops that watch a session select on it.
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) end() {
	s.stopOnce.Do(func() { close(s.done) })
}

func (s *Session) ended() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Touch records activity for the idle clock.
func (s *Session) Touch() {
	s.mu.Lock()
	s.lastUsed = s.clock()
	s.mu.Unlock()
}

// LastUsed is the last activity the service saw.
func (s *Session) LastUsed() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsed
}

// Viewers is how many terminals are open on the session.
func (s *Session) Viewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewers
}

// Idle: nobody is watching and nothing has happened for limit. A fact about
// the session; who acts on it, and when, is the caller's business.
func (s *Session) Idle(now time.Time, limit time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewers == 0 && !s.lastUsed.IsZero() && now.Sub(s.lastUsed) > limit
}

// Active: watched, or used within the window. Purge and remediation ask this.
func (s *Session) Active(now time.Time, within time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewers > 0 || (!s.lastUsed.IsZero() && now.Sub(s.lastUsed) < within)
}

// UsesWorkspace: the session's checkout is this workspace or lies under it.
func (s *Session) UsesWorkspace(workspace string) bool {
	workspace = filepath.Clean(workspace)
	checkout := filepath.Clean(s.Checkout)
	return checkout == workspace || strings.HasPrefix(checkout, workspace+string(os.PathSeparator))
}

// --- the record: pi's session files, read through the transcript ---------------

// record shows the turns kept in step with the session: a chat that
// continues a lane agent's own session shows only its own turns.
// Two requests can read one session at once (concurrent opens of a change
// share its chat), so the fields are set once, not on every read.
func (s *Session) record() *Transcript {
	s.recOnce.Do(func() {
		s.rec.Dir = s.Dir
		if s.AgentSession != "" && s.AgentSession != AgentSession {
			s.rec.Since = s.CreatedAt
		}
	})
	return &s.rec
}

// Records is every session file the conversation owns, oldest first: what
// usage accounting reads. A folder that cannot be read is an error.
func (s *Session) Records() ([]string, error) { return s.record().SessionFiles() }

// Messages is the conversation as chat bubbles, oldest first.
func (s *Session) Messages() []Message { return s.record().Messages() }

// ReadMessages is Messages, with the failure to read the record when there
// was one.
func (s *Session) ReadMessages() ([]Message, error) { return s.record().ReadMessages() }

// HandedBack: the agent has finished its turn.
func (s *Session) HandedBack() bool { return s.record().TurnHandedBack() }

// Turn is the agent's turn state for the chat page.
func (s *Session) Turn() Turn {
	if s.HandedBack() {
		return HandedBack
	}
	return Working
}

// Activity is the turn as another page reads it: empty before the first
// message, so a chat nobody has spoken in is not said to be working.
func (s *Session) Activity() Turn {
	if len(s.Messages()) == 0 {
		return ""
	}
	return s.Turn()
}

// RecordOffset is the transcript reader's position in the newest record,
// for tests of the resume.
func (s *Session) RecordOffset() int64 { return s.record().Offset() }

// LatestRecord is the newest session file and its size: the marker delivery
// verification measures growth past.
func (s *Session) LatestRecord() (string, int64) { return s.record().LatestSessionFile() }

// UserMessageAppended: has a user message entered the record since the marker?
func (s *Session) UserMessageAppended(file string, off int64) bool {
	return s.record().UserMessageAppended(file, off)
}

// --- the record on disk ---------------------------------------------------------

func (s *Session) metaPath() string { return filepath.Join(s.Dir, MetaFile) }

// persist writes session.json: the one place the file is written. 0600, as
// the token inside is the session's credential and the dir is mounted into
// the container. Write-then-rename: a crash mid-write leaves the old record.
func (s *Session) persist() error {
	s.mu.Lock()
	meta := s.Meta
	meta.Labels = maps.Clone(s.Labels)
	meta.LastUsed = s.lastUsed
	s.mu.Unlock()
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp := s.metaPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.metaPath())
}

// Persist writes the session's record. Callers persist when the moment is
// worth surviving a restart (a message delivered, a viewer gone).
func (s *Session) Persist() error { return s.persist() }

// readMeta reads a live session's record from its directory.
func readMeta(dir string) (Meta, error) { return readRecord(filepath.Join(dir, MetaFile)) }

// readAnyMeta reads a session's record, live or ended.
func readAnyMeta(dir string) (Meta, error) {
	meta, err := readMeta(dir)
	if os.IsNotExist(err) {
		return readRecord(filepath.Join(dir, EndedFile))
	}
	return meta, err
}

func readRecord(path string) (Meta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, err
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// SetLastUsed sets the idle clock; recovery and tests use it.
func (s *Session) SetLastUsed(t time.Time) {
	s.mu.Lock()
	s.lastUsed = t
	s.mu.Unlock()
}

// --- the prompt: one file, one writer --------------------------------------------
//
// The agent reads SteeringFile. The console composes it from the authored
// steering handed to Open, the provision's notes (replaced at every wake, since
// what was provisioned moves), and the notices the console has given the
// agent since (resumes). Nobody else writes it.

const notesMarker = "\n<!-- console:provision -->\n"

func (s *Session) composeSteering(notes string) error {
	authored, err := os.ReadFile(filepath.Join(s.Dir, AuthoredFile))
	if err != nil {
		return err
	}
	text := string(authored)
	if strings.TrimSpace(notes) != "" {
		text += notesMarker + notes + notesMarker
	}
	if notices, err := os.ReadFile(filepath.Join(s.Dir, NoticesFile)); err == nil {
		text += string(notices)
	}
	return os.WriteFile(filepath.Join(s.Dir, SteeringFile), []byte(text), 0o644)
}

// notice tells the agent something at its next launch; numbered, dated,
// kept, and appended to the prompt at once so a launch that follows sees it.
func (s *Session) notice(text string) error {
	path := filepath.Join(s.Dir, NoticesFile)
	prior, _ := os.ReadFile(path)
	n := strings.Count(string(prior), "\n## Notice ") + 1
	line := fmt.Sprintf("\n## Notice %d (%s)\n\n%s\n", n, s.clock().UTC().Format(time.RFC3339), text)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	f, err = os.OpenFile(filepath.Join(s.Dir, SteeringFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line)
	return errors.Join(err, f.Close())
}

// Steering is the prompt as the agent reads it now.
func (s *Session) Steering() string {
	b, _ := os.ReadFile(filepath.Join(s.Dir, SteeringFile))
	return string(b)
}

// --- staged files: what the last provision placed, replaced each wake ----------

// stage replaces what the last provision placed with files. Only a session
// with no manifest yet starts from nothing: a manifest that cannot be read,
// or a file it names that cannot be removed, fails the wake, since a
// configuration or credential taken away would stay and drop out of the
// inventory that removes it later.
func stage(dir string, files map[string]Staged) error {
	manifest := filepath.Join(dir, StagedFile)
	var previous []string
	raw, err := os.ReadFile(manifest)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("reading the staged-file inventory: %w", err)
	default:
		if err := json.Unmarshal(raw, &previous); err != nil {
			return fmt.Errorf("reading the staged-file inventory: %w", err)
		}
	}
	for _, rel := range previous {
		if _, keep := files[rel]; !keep {
			if err := removeStaged(filepath.Join(dir, rel)); err != nil {
				return err
			}
		}
	}
	names := make([]string, 0, len(files))
	for rel, f := range files {
		if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return fmt.Errorf("staged file %q: paths are relative, inside the session", rel)
		}
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := removeStaged(path); err != nil {
			return err
		}
		if f.Link != "" {
			if err := os.Symlink(f.Link, path); err != nil {
				return err
			}
		} else {
			mode := f.Mode
			if mode == 0 {
				mode = 0o644
			}
			if err := os.WriteFile(path, f.Data, mode); err != nil {
				return err
			}
		}
		names = append(names, rel)
	}
	sort.Strings(names)
	raw, err = json.Marshal(names)
	if err != nil {
		return err
	}
	return os.WriteFile(manifest, raw, 0o644)
}

// removeStaged removes one staged file; one already gone is removed.
func removeStaged(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the staged file %s: %w", filepath.Base(path), err)
	}
	return nil
}
