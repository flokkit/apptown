package console

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Dependencies is what Console needs and does not own.
type Dependencies struct {
	Agent       Agent       // the runtime the agent runs in
	Environment Environment // the provision for a launch (model, env, mounts); nil runs sessions as opened
	// Settle is what an end does with the session's last record before the
	// session is archived or removed (the opener's accounting of it). It runs
	// after the agent's runtime has ended, so the runtime's last record is
	// in; an error keeps the session registered and its record in place. Nil
	// settles nothing.
	Settle func(ctx context.Context, s *Session) error
	Config Config
}

// Config is the console's own settings.
type Config struct {
	DataDir string // session records live at <DataDir>/console/<id>
	NoDind  bool   // change consoles get no private Docker daemon
	// Mirror answers the Docker Hub mirror a private daemon and its
	// builder pull through ("" none), read at each launch. Nil is none.
	Mirror func(context.Context) string
	// Now is the console's clock: a session's last use and the idle and
	// busy rules judged against it, and the times its records carry. Nil
	// is the real clock.
	Now func() time.Time
}

var (
	ErrNoSession = fault.New(fault.NotFound, "no such session")
	ErrEnded     = errors.New("console session is stopped")
	// ErrAsleep: watching a terminal never starts the agent.
	ErrAsleep = fault.New(fault.Conflict, "this chat is not running; it starts again when someone who may work on it says something")
	// ErrWatching: a watcher's terminal takes no keystrokes.
	ErrWatching = fault.New(fault.Forbidden, "this terminal is open to watch, not to type")
)

// Service owns the sessions: their records on disk, the registry in memory,
// and every operation on one. Built once.
type Service struct {
	agent  Agent
	env    Environment
	settle func(context.Context, *Session) error
	cfg    Config
	now    func() time.Time

	mu       sync.Mutex
	sessions map[string]*Session
	blocked  map[string]bool // label=value being removed: no admission, no identity
	opening  map[string]int  // sessions being opened, per label=value
	pending  map[string]int  // session ids held not ended (Hold), per holder
}

func New(deps Dependencies) *Service {
	return &Service{agent: deps.Agent, env: deps.Environment, settle: deps.Settle, cfg: deps.Config, now: clock.Or(deps.Config.Now),
		sessions: map[string]*Session{}, blocked: map[string]bool{}, opening: map[string]int{}, pending: map[string]int{}}
}

func (s *Service) root() string { return filepath.Join(s.cfg.DataDir, "console") }

// OpenRequest is everything a session needs; it looks nothing up.
type OpenRequest struct {
	// ID is the session's id, from NewSessionID, when the opener must name
	// the session before it exists (to take a change for it); empty picks
	// one.
	ID        string
	Scope     string            // what the opener groups sessions by
	Labels    map[string]string // the opener's facts, kept and handed back
	Workspace Workspace         // the checkout to mount; the caller made it
	Docker    bool              // a private Docker daemon for the agent's own builds
	Steering  string            // the authored prompt
	Actor     string
	Model     string // the model the chat opens on
	Pinned    bool   // the person named it; otherwise the environment's answer wins
	Resume    *Resumption
}

// Resumption continues another agent's session: its record file is copied
// in and resumed, and the header says so.
type Resumption struct {
	// Record is the other agent's record itself, as its owner kept it, for
	// the session Session; it is written as that session's file. Empty
	// starts a fresh agent.
	Record  []byte
	Session string
	Label   string // plain words for the header
}

// Open creates a session: its directory and record, then its agent (created,
// not yet started; the first message or terminal starts it).
func (s *Service) Open(ctx context.Context, in OpenRequest) (*Session, error) {
	if in.Scope == "" {
		return nil, fmt.Errorf("a session needs a scope")
	}
	if in.Workspace.Path == "" {
		return nil, fmt.Errorf("a session needs a workspace")
	}
	release, err := s.reserve(in.Labels)
	if err != nil {
		return nil, err
	}
	defer release()

	id := in.ID
	if id == "" {
		id = s.NewSessionID()
	} else if !validSessionID(id) {
		return nil, fmt.Errorf("session id %q is not one NewSessionID makes", id)
	}
	dir := filepath.Join(s.root(), id)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("session %s already exists", id)
	}
	for _, d := range []string{"scratch", "sessions", "uploads"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return nil, err
		}
	}
	registered := false
	defer func() {
		if !registered {
			_ = os.RemoveAll(dir)
		}
	}()
	meta := Meta{ID: id, Scope: in.Scope, Labels: maps.Clone(in.Labels), Model: in.Model, ModelPinned: in.Pinned, Actor: in.Actor,
		CreatedAt: s.now(), Token: randomHex(24), Checkout: in.Workspace.Path, OwnsCheckout: in.Workspace.Owned, Writable: in.Workspace.Writable, Docker: in.Docker}
	if c := in.Resume; c != nil {
		meta.ResumedFrom = c.Label
		if c.Record != nil {
			if c.Session == "" || filepath.Base(c.Session) != c.Session || strings.HasPrefix(c.Session, ".") {
				return nil, fmt.Errorf("the lane agent's session %q cannot name a record", c.Session)
			}
			name := s.now().UTC().Format("2006-01-02T15-04-05-000Z") + "_" + c.Session + ".jsonl"
			if err := os.WriteFile(filepath.Join(dir, "sessions", name), c.Record, 0o644); err != nil {
				return nil, err
			}
			meta.AgentSession = c.Session
		}
	}
	if err := os.WriteFile(filepath.Join(dir, AuthoredFile), []byte(in.Steering), 0o644); err != nil {
		return nil, err
	}
	sess := newSession(meta, dir, s.now)
	// The record goes down before the runtime: a crash during provisioning
	// is recoverable and cannot look like an orphan to reaping.
	if err := sess.persist(); err != nil {
		return nil, fmt.Errorf("recording the session: %w", err)
	}
	launch, err := s.launch(ctx, sess)
	if err != nil {
		return nil, err
	}
	// A failed open removes what it started even when its caller has gone.
	if err := s.agent.Start(ctx, launch); err != nil {
		_ = s.agent.Remove(context.WithoutCancel(ctx), id)
		return nil, err
	}
	if err := sess.persist(); err != nil {
		_ = s.agent.Remove(context.WithoutCancel(ctx), id)
		return nil, fmt.Errorf("recording the session: %w", err)
	}
	s.mu.Lock()
	s.sessions[id] = sess
	registered = true
	s.mu.Unlock()
	return sess, nil
}

// launch is what the agent runs for the session now: the session's facts
// plus the environment's provision. A provision may move the model.
func (s *Service) launch(ctx context.Context, sess *Session) (Launch, error) {
	l := Launch{ID: sess.ID, Dir: sess.Dir, Actor: sess.Actor, Token: sess.Token,
		Workspace: sess.Workspace(), AgentSession: sess.AgentSession, Model: sess.Model, Docker: sess.Meta.Docker && !s.cfg.NoDind}
	if l.AgentSession == "" {
		l.AgentSession = AgentSession
	}
	if l.Docker && s.cfg.Mirror != nil {
		l.Mirror = s.cfg.Mirror(ctx)
	}
	var p Provision
	if s.env != nil {
		var err error
		if p, err = s.env.Prepare(ctx, sess); err != nil {
			return Launch{}, err
		}
	}
	if p.Model != "" && p.Model != sess.Model {
		sess.mu.Lock()
		sess.Model = p.Model
		sess.mu.Unlock()
		sess.save("its model")
	}
	if err := stage(sess.Dir, p.Files); err != nil {
		return Launch{}, err
	}
	if err := sess.composeSteering(p.Notes); err != nil {
		return Launch{}, err
	}
	l.Model, l.Env, l.Mounts = sess.Model, p.Env, p.Mounts
	return l, nil
}

// wake has the agent running for the session, rebuilt first when what runs
// went stale and the agent has handed back its turn (never mid-turn). A
// restart after a stop is never silent: the prompt gets a resume notice
// before the agent starts, so it knows its shell state is gone.
func (s *Service) wake(ctx context.Context, sess *Session) error {
	// A wake is admitted only while no end is under way, and an end refuses
	// while a wake it admitted is still starting the runtime.
	s.mu.Lock()
	if sess.ended() || sess.ending {
		s.mu.Unlock()
		return ErrEnded
	}
	sess.waking++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		sess.waking--
		s.mu.Unlock()
	}()
	st, err := s.agent.Status(ctx, sess.ID)
	if err != nil {
		return err
	}
	if st.Exists && !st.Running && st.EverStarted {
		if err := sess.notice(resumeNotice); err != nil {
			return err
		}
	}
	launch, err := s.launch(ctx, sess)
	if err != nil {
		return err
	}
	stale, err := s.agent.Stale(ctx, launch)
	if err != nil {
		return err
	}
	if stale && (!st.Running || sess.HandedBack()) {
		if err := s.agent.Remove(ctx, sess.ID); err != nil {
			return err
		}
	}
	return s.agent.Start(ctx, launch)
}

const resumeNotice = "This session resumed in a restarted container. Background processes and shell state " +
	"from earlier in the conversation are GONE; files under /console and /work, and tools installed in the " +
	"container filesystem, persist. Re-query the system's state (things may have moved) rather than trusting earlier answers."

// Get is the session by id.
func (s *Service) Get(ctx context.Context, id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return nil, ErrNoSession
	}
	return sess, nil
}

// admit is Get for activity: refused while the project is being deleted,
// and it touches the idle clock.
func (s *Service) admit(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil || s.isBlocked(sess.Meta) {
		return nil, ErrNoSession
	}
	sess.Touch()
	return sess, nil
}

// List is every session, or those a Match selects.
func (s *Service) List(ctx context.Context, m Match) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Session
	for _, sess := range s.sessions {
		if !m.matches(sess.Meta) {
			continue
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// State is where the session stands on the ladder.
func (s *Service) State(ctx context.Context, sess *Session) State {
	st, err := s.agent.Status(ctx, sess.ID)
	if err != nil {
		return Unavailable
	}
	if !st.Exists {
		return Dead
	}
	if st.Attached {
		if sess.Viewers() > 0 {
			return Attached
		}
		return Detached
	}
	if st.Running {
		return Detached
	}
	return Stopped
}

// MessagePage is the conversation from a bubble index on, with the turn state.
type MessagePage struct {
	Messages []Message
	Turn     Turn
}

// Messages is the conversation from bubble `from` on. Reading keeps the
// session warm: an open chat page is a viewer in spirit.
func (s *Service) Messages(ctx context.Context, id string, from int) (MessagePage, error) {
	sess, err := s.admit(id)
	if err != nil {
		return MessagePage{}, err
	}
	all := sess.Messages()
	out := make([]Message, 0, len(all))
	for _, m := range all {
		if m.I >= from {
			out = append(out, m)
		}
	}
	return MessagePage{Messages: out, Turn: sess.Turn()}, nil
}

// Warm has the agent running for the session without saying anything: an
// open chat page keeps its helper ready to answer.
func (s *Service) Warm(ctx context.Context, id string) error {
	sess, err := s.admit(id)
	if err != nil {
		return err
	}
	return s.wake(ctx, sess)
}

// Send types words at the agent, waking it first. Newlines fold to spaces:
// a raw newline would submit mid-thought.
func (s *Service) Send(ctx context.Context, id, text string) error {
	sess, err := s.admit(id)
	if err != nil {
		return err
	}
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	if text == "" {
		return fmt.Errorf("say something")
	}
	if err := s.wake(ctx, sess); err != nil {
		return fmt.Errorf("waking the helper: %w", err)
	}
	launch, err := s.launch(ctx, sess)
	if err != nil {
		return err
	}
	if err := s.agent.Deliver(ctx, launch, text); err != nil {
		return err
	}
	sess.Touch()
	return sess.persist()
}

// Interrupt stops the agent's current turn; the conversation and the
// workspace are untouched.
func (s *Service) Interrupt(ctx context.Context, id string) error {
	sess, err := s.admit(id)
	if err != nil {
		return err
	}
	if err := s.wake(ctx, sess); err != nil {
		return fmt.Errorf("waking the helper: %w", err)
	}
	if err := s.agent.Interrupt(ctx, sess.ID); err != nil {
		return err
	}
	sess.Touch()
	return sess.persist()
}

// Attach opens a terminal on the session, waking the agent first. The
// terminal counts as a viewer until it is closed.
func (s *Service) Attach(ctx context.Context, id string, size Size) (Terminal, error) {
	sess, err := s.admit(id)
	if err != nil {
		return nil, err
	}
	if err := s.wake(ctx, sess); err != nil {
		return nil, err
	}
	term, err := s.agent.Attach(ctx, sess.ID, size)
	if err != nil {
		return nil, err
	}
	sess.mu.Lock()
	sess.viewers++
	sess.mu.Unlock()
	return &viewer{Terminal: term, sess: sess}, nil
}

// Watch opens a terminal on the session to watch its output: the screen
// replayed, then live. It does not wake the agent, touch the idle clock or
// count as a viewer, and it takes no keystrokes or resizes. Watching a chat
// whose agent is not running is refused (ErrAsleep).
func (s *Service) Watch(ctx context.Context, id string) (Terminal, error) {
	s.mu.Lock()
	sess := s.sessions[id]
	blocked := sess == nil || s.isBlocked(sess.Meta)
	s.mu.Unlock()
	if blocked {
		return nil, ErrNoSession
	}
	st, err := s.agent.Status(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	if !st.Running {
		return nil, ErrAsleep
	}
	term, err := s.agent.Attach(ctx, sess.ID, Size{})
	if err != nil {
		return nil, err
	}
	return watcher{term}, nil
}

// watcher is a terminal that only shows.
type watcher struct{ Terminal }

func (watcher) Write([]byte) (int, error) { return 0, ErrWatching }
func (watcher) Resize(Size) error         { return nil }

// viewer is a terminal that counts as a viewer while open.
type viewer struct {
	Terminal
	sess *Session
	once sync.Once
}

func (v *viewer) Write(p []byte) (int, error) {
	v.sess.Touch()
	return v.Terminal.Write(p)
}

func (v *viewer) Close() error {
	v.once.Do(func() {
		v.sess.mu.Lock()
		v.sess.viewers--
		v.sess.lastUsed = v.sess.clock()
		v.sess.mu.Unlock()
		v.sess.save("a viewer leaving") // the idle clock's reference point
	})
	return v.Terminal.Close()
}

// Stop stops the agent and keeps everything else: the record, the files,
// the conversation. The next message or terminal wakes it.
func (s *Service) Stop(ctx context.Context, id string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	sess.save("its stop")
	return s.agent.Stop(ctx, sess.ID)
}

// Ended is what End leaves for the caller to finish: a change console's
// worktree is the change's and stays; a project console's snapshot is
// deleted here.
type Ended struct {
	Session *Session
}

// End removes the session: the agent's runtime and the record. What
// remains under the session dir (the session files, the steering) is the
// archived conversation.
func (s *Service) End(ctx context.Context, id string) (Ended, error) {
	s.mu.Lock()
	sess := s.sessions[id]
	switch {
	case sess == nil:
		s.mu.Unlock()
		return Ended{}, ErrNoSession
	case sess.ending:
		s.mu.Unlock()
		return Ended{}, fmt.Errorf("chat %s is already ending", id)
	case sess.waking > 0:
		s.mu.Unlock()
		return Ended{}, fmt.Errorf("chat %s is starting its agent; end it again once it has started", id)
	}
	sess.ending = true
	s.mu.Unlock()
	// The runtime is removed first: a chat whose agent may still run stays
	// live, and ending it can be asked again.
	if err := s.agent.End(ctx, sess.ID); err != nil {
		s.mu.Lock()
		sess.ending = false
		s.mu.Unlock()
		return Ended{}, fmt.Errorf("removing chat %s's agent: %w; the chat stays until it is removed", sess.ID, err)
	}
	// Settled after the runtime's end (its last record is in) and before the
	// record is archived or removed: a chat whose settling failed keeps its
	// registration and its record, and is ended again.
	if s.settle != nil {
		if err := s.settle(ctx, sess); err != nil {
			s.mu.Lock()
			sess.ending = false
			s.mu.Unlock()
			return Ended{}, fmt.Errorf("chat %s ended its agent, but settling it failed: %w; it stays until it is ended again", sess.ID, err)
		}
	}
	// The record becomes the archive: recovery no longer sees a live
	// session, the transcript still finds the conversation. Until it has,
	// the chat stays live, and ending it again finishes the end.
	if err := os.Rename(sess.metaPath(), filepath.Join(sess.Dir, EndedFile)); err != nil {
		s.mu.Lock()
		sess.ending = false
		s.mu.Unlock()
		return Ended{}, fmt.Errorf("chat %s's agent is removed, but its record could not be marked ended: %w; end it again", sess.ID, err)
	}
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	sess.end()
	if sess.OwnsCheckout {
		_ = os.RemoveAll(sess.Checkout)
	}
	return Ended{Session: sess}, nil
}

// Gone reports whether a session is over and nothing of it can still write
// its workspace: it is neither registered nor recorded as live (it ended,
// or never opened), and neither its
// agent nor its private Docker daemon exists. A runtime left behind by an
// end whose removal failed is removed again first; the answer is never
// assumed.
func (s *Service) Gone(ctx context.Context, id string) (bool, error) {
	return s.gone(ctx, id, true)
}

// Ended is Gone without removing anything: it reports whether the session
// is over and its runtime already absent, for a check that must change
// nothing.
func (s *Service) Ended(ctx context.Context, id string) (bool, error) {
	return s.gone(ctx, id, false)
}

func (s *Service) gone(ctx context.Context, id string, remove bool) (bool, error) {
	s.mu.Lock()
	_, live := s.sessions[id]
	live = live || s.pending[id] > 0
	s.mu.Unlock()
	if live || !validSessionID(id) {
		return false, nil
	}
	// A session whose live record is on disk has not ended, whether or not
	// this process has restored it yet.
	if _, err := os.Stat(filepath.Join(s.root(), id, MetaFile)); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	st, err := s.agent.Status(ctx, id)
	if err != nil {
		return false, err
	}
	if !st.Exists && !st.SidecarExists && !st.ScriptExists {
		return true, nil
	}
	if !remove {
		return false, nil
	}
	if err := s.agent.End(ctx, id); err != nil {
		return false, err
	}
	st, err = s.agent.Status(ctx, id)
	return err == nil && !st.Exists && !st.SidecarExists && !st.ScriptExists, err
}

// Recorded reads a session's record from disk, live or ended, whether or
// not this process has restored it: what it was opened with, including the
// opener's labels.
func (s *Service) Recorded(id string) (Meta, error) {
	if !validSessionID(id) {
		return Meta{}, ErrNoSession
	}
	meta, err := readAnyMeta(filepath.Join(s.root(), id))
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, ErrNoSession
	}
	return meta, err
}

// Hold keeps id from counting as ended until release, whatever its record
// and runtime show: an opener that has claimed something for a session not
// yet opened (a change), or an ending that still has work to settle for it
// (publishing its commits). After release, Gone judges the session by its
// record and runtime as usual.
func (s *Service) Hold(id string) func() {
	s.mu.Lock()
	s.pending[id]++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.pending[id]--; s.pending[id] <= 0 {
				delete(s.pending, id)
			}
			s.mu.Unlock()
		})
	}
}

// Evict takes the agent's runtime away and keeps the session registered:
// the reaper's move for a long-idle chat. The next message rebuilds it.
func (s *Service) Evict(ctx context.Context, id string) error {
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	sess.save("its eviction")
	return s.agent.Remove(ctx, sess.ID)
}

// Restore registers a session and writes its live record without touching
// its agent: what Resume finds per record, offered to tests that build
// sessions by hand. A registered chat always has its record (End archives
// it); a test whose folder cannot hold one panics here.
func (s *Service) Restore(meta Meta) *Session {
	dir := filepath.Join(s.root(), meta.ID)
	sess := newSession(meta, dir, s.now)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(fmt.Sprintf("restoring chat %s: %v", meta.ID, err))
	}
	if err := sess.persist(); err != nil {
		panic(fmt.Sprintf("restoring chat %s: %v", meta.ID, err))
	}
	s.mu.Lock()
	s.sessions[meta.ID] = sess
	s.mu.Unlock()
	return sess
}

// Remove ends the session and deletes everything it left on disk: purge.
func (s *Service) Remove(ctx context.Context, id string) error {
	ended, err := s.End(ctx, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(ended.Session.Dir)
}

// Upload stores a pasted picture under the session, server-named, and says
// where the agent will find it.
type File struct {
	Ext  string // png, jpg, webp, gif
	Body []byte
}

type Attachment struct {
	Name string // picture-N.ext
	Path string // where the agent sees it: UploadsMount/name
}

func (s *Service) Upload(ctx context.Context, id string, f File) (Attachment, error) {
	sess, err := s.admit(id)
	if err != nil {
		return Attachment{}, err
	}
	dir := filepath.Join(sess.Dir, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Attachment{}, err
	}
	entries, _ := os.ReadDir(dir)
	name := fmt.Sprintf("picture-%d.%s", len(entries)+1, f.Ext)
	if err := os.WriteFile(filepath.Join(dir, name), f.Body, 0o644); err != nil {
		return Attachment{}, err
	}
	return Attachment{Name: name, Path: UploadsMount + "/" + name}, nil
}

// ErrNoPicture: the chat has no picture by that name, or the name is not one
// the console gives.
var ErrNoPicture = errors.New("no such picture")

var pictureName = regexp.MustCompile(`^picture-\d+\.(png|jpg|webp|gif)$`)

// ValidPictureName reports whether name is one Upload gives a pasted
// picture: never a path.
func ValidPictureName(name string) bool { return pictureName.MatchString(name) }

// RecordedPicture is where a pasted picture of a scope's chat lives on disk,
// live or ended, read from the chat's record without registering or waking
// it: the chat's words stay readable after it ends, and so do its pictures.
// The chat must belong to the scope and the name must be one Upload gives;
// nothing is listed.
func (s *Service) RecordedPicture(scope, id, name string) (string, error) {
	if !validSessionID(id) || !ValidPictureName(name) {
		return "", ErrNoPicture
	}
	dir := filepath.Join(s.root(), id)
	meta, err := readAnyMeta(dir)
	if err != nil || meta.Scope != scope {
		return "", ErrNoPicture
	}
	// Whether the file is there, and that reading it stays inside the
	// chat's uploads, is the serving read's (httpapi.ServeUntrustedFile).
	return filepath.Join(dir, "uploads", name), nil
}

// RemoveEnded deletes the folders of the ended sessions a Match selects,
// read from their records without registering or waking them: a deleted
// project's ended chats, words and pictures. A live session is left to
// Remove, which ends it first.
func (s *Service) RemoveEnded(m Match) error {
	// A folder whose record cannot be read cannot be known to match: it is
	// left for a person, never deleted on a guess.
	records, _, err := s.RecordsMatching(m)
	if err != nil {
		return err
	}
	var failed []error
	for _, r := range records {
		if r.Ended && !r.Live {
			if err := os.RemoveAll(r.Dir); err != nil {
				failed = append(failed, err)
			}
		}
	}
	return errors.Join(failed...)
}

// UploadPath is where a named upload lives on disk, for serving it back.
func (s *Service) UploadPath(ctx context.Context, id, name string) (string, error) {
	sess, err := s.admit(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(sess.Dir, "uploads", name), nil
}

// Annotate keeps one of the opener's facts on the session's record.
func (s *Service) Annotate(ctx context.Context, id, key, value string) error {
	sess, err := s.admit(id)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	if sess.Labels == nil {
		sess.Labels = map[string]string{}
	}
	sess.Labels[key] = value
	sess.lastUsed = sess.clock()
	sess.mu.Unlock()
	return sess.persist()
}

// RuntimeIdentity is who the token's holder is: the agent inside a session,
// with the opener's scope and facts.
type RuntimeIdentity struct {
	SessionID string
	Scope     string
	Labels    map[string]string
	Actor     string
}

// Identity resolves a session token: the credential the agent inside holds.
// A session on a project being deleted identifies nobody; so does an ended one.
func (s *Service) Identity(token string) (RuntimeIdentity, bool) {
	if token == "" {
		return RuntimeIdentity{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if s.isBlocked(sess.Meta) || sess.ended() {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(sess.Token), []byte(token)) == 1 {
			sess.Touch()
			sess.mu.Lock()
			labels := maps.Clone(sess.Labels)
			sess.mu.Unlock()
			return RuntimeIdentity{SessionID: sess.ID, Scope: sess.Scope, Labels: labels, Actor: sess.Actor}, true
		}
	}
	return RuntimeIdentity{}, false
}

// Busy: a session the match selects is watched or was used within the window.
func (s *Service) Busy(ctx context.Context, m Match, within time.Duration) bool {
	now := s.now()
	for _, sess := range s.List(ctx, m) {
		if sess.Active(now, within) {
			return true
		}
	}
	return false
}

// UsesWorkspace: a live session's checkout is this workspace or lies under it.
func (s *Service) UsesWorkspace(ctx context.Context, workspace string) bool {
	for _, sess := range s.List(ctx, Match{}) {
		if sess.UsesWorkspace(workspace) {
			return true
		}
	}
	return false
}

// Conversation is one session's words, for a change's or a project's chat
// history.
type Conversation struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Actor     string    `json:"actor"`
	Messages  []Message `json:"messages"`
	// Ended: the session's record is its archive (EndedFile), not a live one.
	Ended bool `json:"-"`
	// Labels are the opener's, handed back unread, as on Meta.
	Labels map[string]string `json:"-"`
}

// Transcript is every conversation a scope has had, oldest first, read from
// the records on disk: sessions end and their runtimes go, the words stay.
// A chat folder whose record or words cannot be read is named in
// unreadable, never left out silently: its scope may be this one. One bad
// folder does not stop the others being read.
func (s *Service) Transcript(ctx context.Context, scope string) (convs []Conversation, unreadable []string, err error) {
	if scope == "" {
		return []Conversation{}, nil, nil
	}
	entries, err := os.ReadDir(s.root())
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	out := []Conversation{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.root(), e.Name())
		meta, found, err := recordedMeta(dir)
		if err != nil {
			unreadable = append(unreadable, e.Name())
			continue
		}
		if !found || meta.Scope != scope {
			continue
		}
		rec := &Session{Meta: meta, Dir: dir}
		msgs, err := rec.ReadMessages()
		if err != nil {
			unreadable = append(unreadable, e.Name())
			continue
		}
		if len(msgs) == 0 {
			continue
		}
		out = append(out, Conversation{ID: meta.ID, CreatedAt: meta.CreatedAt, Actor: meta.Actor, Messages: msgs,
			Ended: archived(dir), Labels: meta.Labels})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, unreadable, nil
}

// recordedMeta reads a session folder's record, live or ended, for a scan
// of the records on disk: a folder with no record is no session (found is
// false), and a record that cannot be read or parsed is an error naming it,
// never taken for no session.
func recordedMeta(dir string) (Meta, bool, error) {
	meta, err := readAnyMeta(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, false, nil
	}
	if err != nil {
		return Meta{}, false, fmt.Errorf("reading the chat record in %s: %w", dir, err)
	}
	return meta, true, nil
}

// archived reports whether a session's record is its archive: no live
// record is on disk. A live record that cannot be read is not known to have
// ended.
func archived(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, MetaFile))
	return errors.Is(err, os.ErrNotExist)
}

// Recording is a session's record on disk, live or ended, with its
// directory, whether it is live in this process, and whether its record is
// its archive (it ended).
type Recording struct {
	Meta
	Dir   string
	Live  bool
	Ended bool
}

// ReadMessages are the recorded session's words, read from its record; a
// record that cannot be read is an error, never no words.
func (r Recording) ReadMessages() ([]Message, error) {
	return (&Session{Meta: r.Meta, Dir: r.Dir}).ReadMessages()
}

// Records is every session a scope has had, live or ended, oldest first,
// read from the records on disk, and the folders whose record could not be
// read (RecordsMatching).
func (s *Service) Records(scope string) ([]Recording, []string, error) {
	return s.RecordsMatching(Match{Scope: scope})
}

// RecordsMatching is every session a Match selects, live or ended, oldest
// first, read from the records on disk: an ended session is selected by
// its archived record, without registering or waking it. A folder whose
// record cannot be read or parsed may belong to the Match, so it is named
// in unreadable, never skipped silently; the caller decides what that
// means for it.
func (s *Service) RecordsMatching(m Match) (records []Recording, unreadable []string, err error) {
	entries, err := os.ReadDir(s.root())
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	var out []Recording
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.root(), e.Name())
		meta, found, err := recordedMeta(dir)
		if err != nil {
			unreadable = append(unreadable, e.Name())
			continue
		}
		if !found || !m.matches(meta) {
			continue
		}
		s.mu.Lock()
		_, live := s.sessions[meta.ID]
		s.mu.Unlock()
		out = append(out, Recording{Meta: meta, Dir: dir, Live: live, Ended: archived(dir)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, unreadable, nil
}

// --- admission: opening and deleting -------------------------------------------

// Reserve is the admission an Open holds while it provisions: a block on
// any of its labels admitted meanwhile is refused. Open takes it itself.
func (s *Service) Reserve(labels map[string]string) (func(), error) { return s.reserve(labels) }

func (s *Service) reserve(labels map[string]string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isBlocked(Meta{Labels: labels}) {
		return nil, fmt.Errorf("sessions with these labels are being removed")
	}
	keys := labelKeys(labels)
	for _, k := range keys {
		s.opening[k]++
	}
	return func() {
		s.mu.Lock()
		for _, k := range keys {
			s.opening[k]--
			if s.opening[k] == 0 {
				delete(s.opening, k)
			}
		}
		s.mu.Unlock()
	}, nil
}

// Block admits the removal of everything labelled key=value (a project
// being deleted): refused while such a chat is being opened, is
// mid-conversation, or is ending. Until the release, no such session is opened, admitted
// or identified.
func (s *Service) Block(key, value string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key + "=" + value
	if s.blocked[k] {
		return nil, fmt.Errorf("already being removed")
	}
	if s.opening[k] > 0 {
		return nil, fmt.Errorf("a chat is being opened here — finish or cancel it before deleting")
	}
	now := s.now()
	for _, sess := range s.sessions {
		// An ending chat is still making its last save, which needs what
		// a deletion removes (its change); the deletion waits for it.
		if sess.Labels[key] == value && sess.ending {
			return nil, fmt.Errorf("chat %s is still saving its work as it ends — try again in a moment", sess.ID)
		}
		if sess.Labels[key] == value && sess.Active(now, 10*time.Minute) {
			return nil, fmt.Errorf("a chat here is mid-conversation (session %s) — end the chat first, then delete", sess.ID)
		}
	}
	s.blocked[k] = true
	return func() {
		s.mu.Lock()
		delete(s.blocked, k)
		s.mu.Unlock()
	}, nil
}

func (s *Service) isBlocked(meta Meta) bool {
	for _, k := range labelKeys(meta.Labels) {
		if s.blocked[k] {
			return true
		}
	}
	return false
}

func labelKeys(labels map[string]string) []string {
	out := make([]string, 0, len(labels))
	for k, v := range labels {
		out = append(out, k+"="+v)
	}
	return out
}

// --- boot and shutdown ------------------------------------------------------------

// Resume rebuilds the registry from the records on disk after a restart
// (agents survive restarts; terminals do not), then has the runtime reap
// what belongs to no record. The records are the only inventory of what
// may run, so the reap runs only when every one was read: a chat folder
// that cannot be listed, or a live record that cannot be read or is
// incomplete (kept for repair), is an error and nothing is reaped, since
// its agent may be one of those still running. The readable chats are
// registered either way. A folder with no live record is an ended chat.
func (s *Service) Resume(ctx context.Context) error {
	entries, err := os.ReadDir(s.root())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("listing the chats: %w; nothing is reaped", err)
	}
	alive := map[string]bool{}
	var unknown []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "bin" {
			continue
		}
		dir := filepath.Join(s.root(), e.Name())
		meta, err := readMeta(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue // ended
		case err != nil:
			unknown = append(unknown, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		if meta.ID != e.Name() || meta.Scope == "" || meta.Checkout == "" || meta.LastUsed.IsZero() {
			unknown = append(unknown, e.Name()+": incomplete or mismatched session metadata, preserved for repair")
			continue
		}
		sess := newSession(meta, dir, s.now)
		s.mu.Lock()
		s.sessions[meta.ID] = sess
		s.mu.Unlock()
		alive[meta.ID] = true
	}
	if len(unknown) > 0 {
		return fmt.Errorf("these chat records cannot be read, so whether their agents still run is not known and nothing is reaped: %s", strings.Join(unknown, "; "))
	}
	return s.agent.Reap(ctx, alive)
}

// Shutdown ends the service's hold on every session for a process stop:
// terminals close, agents and records stay for the next Resume.
func (s *Service) Shutdown(ctx context.Context) error {
	for _, sess := range s.List(ctx, Match{}) {
		sess.end()
	}
	return nil
}

// NewSessionID makes a fresh session id: a UUIDv7 in its canonical text,
// minted from the console's clock.
func (s *Service) NewSessionID() string { return ids.New(s.now()).String() }

// validSessionID is whether id is one NewSessionID makes, spelled as it
// spells it: a session's id names its folder.
func validSessionID(id string) bool {
	parsed, err := ids.Parse(id)
	return err == nil && !parsed.IsZero() && parsed.String() == id
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// save writes the session's record to disk, best effort: the chat still
// runs, and its idle clock and a later resume read the last record saved,
// so a failure is logged with what was not saved.
func (s *Session) save(what string) {
	if err := s.persist(); err != nil {
		log.Printf("chat %s: saving its record after %s: %v", s.ID, what, err)
	}
}
