package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/console"
)

// A session is a chat that runs on this worker (design/operations/workers.md,
// "Chats on the worker"): the chat's container and its private Docker daemon, run by the
// same chat runtime AppTown uses (a console.Agent; *pi.Runtime in
// production) on the worker's own Docker, with the chat's files under the
// state directory. AppTown's side is RemoteAgent.
//
// The worker keeps one small record per session. Removing a session marks
// its record removed before anything is taken away, and a removed session is
// never started again, so a start that arrives late cannot bring back a chat
// AppTown has ended. Stopping or rebuilding a session's runtime is not
// removing it: its files and record stay.

var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// agentSessionPattern bounds the agent's own session id, which the chat's
// launcher names on a command line.
var agentSessionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// imagePattern is an image reference: an optional registry host (with an
// optional port), a name, an optional tag and an optional digest. It never
// begins with "-", so it cannot be read as a Docker option.
var imagePattern = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?/)?[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$`)

// SessionSpec is what AppTown asks a session to run: the chat's launch
// without host paths. The worker supplies the paths: a mount names content
// already sent to the worker (a blob), never a path on either machine.
type SessionSpec struct {
	Image        string `json:"image"`
	Model        string `json:"model,omitempty"`
	AgentSession string `json:"agentSession"`
	Docker       bool   `json:"docker,omitempty"`
	// Mirror is the Docker Hub mirror the chat's private daemon and its
	// builder pull through ("" none); its resolvers are the worker's own.
	Mirror string         `json:"mirror,omitempty"`
	Env    []string       `json:"env,omitempty"`
	Mounts []SessionMount `json:"mounts,omitempty"`
}

// SessionMount mounts a blob's content, read-only, at Target in the chat's
// container.
type SessionMount struct {
	Blob   string `json:"blob"`
	Target string `json:"target"`
}

var (
	blobPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	targetPattern = regexp.MustCompile(`^(/[A-Za-z0-9._@+-]+)+$`)
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]{0,199}$`)
	commitPattern = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)
	mirrorPattern = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)
)

func (s SessionSpec) validate() error {
	if len(s.Image) > 255 || !imagePattern.MatchString(s.Image) {
		return errors.New("a session needs an image reference")
	}
	if !agentSessionPattern.MatchString(s.AgentSession) {
		return errors.New("invalid agent session")
	}
	if s.Mirror != "" && (len(s.Mirror) > 255 || !mirrorPattern.MatchString(s.Mirror)) {
		return errors.New("a mirror is an http or https address")
	}
	for _, kv := range s.Env {
		if !envPattern.MatchString(kv) {
			return fmt.Errorf("invalid environment entry %q", strings.SplitN(kv, "=", 2)[0])
		}
	}
	for _, m := range s.Mounts {
		// A mount may not shadow the chat's own files or work tree.
		if !blobPattern.MatchString(m.Blob) || !targetPattern.MatchString(m.Target) || strings.Contains(m.Target, "/..") ||
			m.Target == console.Mount || strings.HasPrefix(m.Target, console.Mount+"/") ||
			m.Target == console.WorkMount || strings.HasPrefix(m.Target, console.WorkMount+"/") {
			return fmt.Errorf("invalid mount at %q", m.Target)
		}
	}
	return nil
}

// sessionRecord is the worker's record of one session.
type sessionRecord struct {
	ID      string      `json:"id"`
	Spec    SessionSpec `json:"spec"`
	Created time.Time   `json:"created"`
	// Removed: AppTown ended the session. The record stays so that the
	// session is never started again.
	Removed bool `json:"removed,omitempty"`
	// FilesDigest identifies the chat's files as last sent.
	FilesDigest string `json:"filesDigest,omitempty"`
	// Source is the commit the chat's work tree was made from; it is made
	// once, before the chat first runs.
	Source string `json:"source,omitempty"`
}

// SessionStatus is the worker's answer about a session. Known is false when
// the worker has no record of it. The runtime's state is read from Docker at
// the time of asking; a worker that cannot read it answers with an error,
// never with absence.
type SessionStatus struct {
	ID      string `json:"id"`
	Known   bool   `json:"known"`
	Removed bool   `json:"removed,omitempty"`
	// Agent is the chat's container; Docker its private daemon.
	Agent struct {
		Exists      bool `json:"exists"`
		Running     bool `json:"running"`
		EverStarted bool `json:"everStarted"`
		// Attached: the worker holds the chat's terminal.
		Attached bool `json:"attached"`
	} `json:"agent"`
	Docker struct {
		Exists  bool `json:"exists"`
		Running bool `json:"running"`
	} `json:"docker"`
	// Script: a save's container exists on the chat's work tree.
	Script struct {
		Exists bool `json:"exists"`
	} `json:"script"`
	// Stale: the container no longer matches the session's spec and is
	// stopped, so a rebuild may make a new one. A running chat is never
	// stale here: it is rebuilt only once it has stopped.
	Stale bool `json:"stale,omitempty"`
	// FilesDigest identifies the chat's files as last sent.
	FilesDigest string `json:"filesDigest,omitempty"`
	// Source is the commit the chat's work tree was made from; empty until
	// AppTown sends it.
	Source string `json:"source,omitempty"`
}

// Gone: nothing of the session runs or remains on the worker's Docker.
func (st SessionStatus) Gone() bool {
	return !st.Agent.Exists && !st.Docker.Exists && !st.Script.Exists
}

// SessionSummary is one line of the worker's list of sessions.
type SessionSummary struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Removed bool      `json:"removed,omitempty"`
}

var errSessionRemoved = errors.New("this session was removed and cannot be started again")

func (s *Server) sessionRecordPath(id string) string {
	return filepath.Join(s.State, "sessions", id+".json")
}

// sessionFiles holds a session's files: its console directory (the chat's
// record, settings and scratch) and its work tree.
func (s *Server) sessionFiles(id string) string { return filepath.Join(s.State, "chats", id) }

func (s *Server) launch(rec sessionRecord) console.Launch {
	files := s.sessionFiles(rec.ID)
	var mounts []string
	for _, m := range rec.Spec.Mounts {
		mounts = append(mounts, s.blobContent(m.Blob)+":"+m.Target+":ro")
	}
	return console.Launch{
		Mounts:       mounts,
		ID:           rec.ID,
		Dir:          filepath.Join(files, "console"),
		Workspace:    console.Workspace{Path: filepath.Join(files, "work"), Writable: true},
		AgentSession: rec.Spec.AgentSession,
		Model:        rec.Spec.Model,
		Docker:       rec.Spec.Docker,
		Mirror:       rec.Spec.Mirror,
		Env:          slices.Clone(rec.Spec.Env),
		Image:        rec.Spec.Image,
	}
}

// lockSession serializes everything done to one session, so a start and a
// removal cannot interleave.
func (s *Server) lockSession(id string) func() {
	s.mu.Lock()
	if s.sessionLocks == nil {
		s.sessionLocks = map[string]*sync.Mutex{}
	}
	m := s.sessionLocks[id]
	if m == nil {
		m = &sync.Mutex{}
		s.sessionLocks[id] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (s *Server) readSession(id string) (sessionRecord, bool, error) {
	body, err := os.ReadFile(s.sessionRecordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return sessionRecord{}, false, nil
	}
	if err != nil {
		return sessionRecord{}, false, err
	}
	var rec sessionRecord
	return rec, true, json.Unmarshal(body, &rec)
}

func (s *Server) writeSession(rec sessionRecord) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeDurable(filepath.Join(s.State, "sessions"), rec.ID+".json", body)
}

// sessionRuntime answers 501 for a worker that runs no chats.
func (s *Server) sessionRuntime(w http.ResponseWriter) bool {
	if s.Sessions == nil {
		http.Error(w, "this worker runs no chats", http.StatusNotImplemented)
		return false
	}
	return true
}

// putSession records a session, or replaces a known session's spec; a
// rebuild or the next start uses it. A removed session is refused.
func (s *Server) putSession(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	id := r.PathValue("id")
	var spec SessionSpec
	// Unknown fields are refused: a spec never carries host paths or mounts.
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		http.Error(w, "invalid session: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := spec.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer s.lockSession(id)()
	rec, ok, err := s.readSession(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rec.Removed {
		http.Error(w, errSessionRemoved.Error(), http.StatusConflict)
		return
	}
	if !ok {
		rec = sessionRecord{ID: id, Created: s.now().UTC()}
	}
	rec.Spec = spec
	if err := s.writeSession(rec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.answerSession(w, r.Context(), rec, true)
}

// knownSession reads a session's record under its lock for an action that
// needs a live one: 404 for no record, 409 for a removed one.
func (s *Server) knownSession(w http.ResponseWriter, id string) (sessionRecord, bool) {
	rec, ok, err := s.readSession(id)
	switch {
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return rec, false
	case !ok:
		http.Error(w, "no such session", http.StatusNotFound)
		return rec, false
	case rec.Removed:
		http.Error(w, errSessionRemoved.Error(), http.StatusConflict)
		return rec, false
	}
	return rec, true
}

// startSession makes the session's container and private daemon run.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	id := r.PathValue("id")
	defer s.lockSession(id)()
	rec, ok := s.knownSession(w, id)
	if !ok {
		return
	}
	in := s.launch(rec)
	// The record's directory exists before the runtime provisions, so the
	// chat's first record file is made for pi to find, as on AppTown.
	for _, dir := range []string{in.Dir, filepath.Join(in.Dir, "sessions"), in.Workspace.Path} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, m := range rec.Spec.Mounts {
		if _, err := os.Stat(s.blobContent(m.Blob)); err != nil {
			http.Error(w, "blob "+m.Blob+" has not been sent", http.StatusUnprocessableEntity)
			return
		}
	}
	if s.Images != nil {
		has, err := s.Images.Has(r.Context(), rec.Spec.Image)
		if err != nil {
			http.Error(w, "checking the chat's image: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !has {
			http.Error(w, "this worker does not have the image "+rec.Spec.Image+
				"; an AppTown-agent image is built by the worker from its own deploy/apptown-agent, so the worker and AppTown may be different versions",
				http.StatusUnprocessableEntity)
			return
		}
	}
	if err := s.Sessions.Start(r.Context(), in); err != nil {
		http.Error(w, "starting the session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.answerSession(w, r.Context(), rec, true)
}

// Stopped: nothing of the chat runs: not its container, its private
// daemon, or a save's container.
func (st SessionStatus) Stopped() bool {
	return !st.Agent.Running && !st.Docker.Running && !st.Script.Exists
}

// stopSession stops the session's runtime; its files stay and a start runs
// it again. It succeeds only once Docker shows nothing of it running.
func (s *Server) stopSession(w http.ResponseWriter, r *http.Request) {
	s.retire(w, r, "stopping", SessionStatus.Stopped, func(ctx context.Context, id string) error { return s.Sessions.Stop(ctx, id) })
}

// rebuildSession removes the session's runtime but keeps the session: its
// files and record stay, and the next start creates the runtime anew from
// the session's spec. It succeeds only once Docker shows the old runtime
// gone, so a start cannot adopt it.
//
// A chat is rebuilt only while it is stopped, by Docker's own answer read
// under the session's lock: a running chat may be in a turn, or about to
// start one from input already typed, and the worker does not guess which.
// A chat on a changed image or model picks it up when it next stops.
func (s *Server) rebuildSession(w http.ResponseWriter, r *http.Request) {
	s.retire(w, r, "rebuilding", SessionStatus.Gone, func(ctx context.Context, id string) error {
		cur, err := s.Sessions.Status(ctx, id)
		if err != nil {
			return err
		}
		if cur.Running {
			return errSessionBusy
		}
		return s.Sessions.Remove(ctx, id)
	})
}

var errSessionBusy = errors.New("the chat is running; it is rebuilt only while stopped")

// retire acts on a live session's runtime and reads Docker back: done must
// hold of what it shows, or the answer is an error and AppTown asks again.
func (s *Server) retire(w http.ResponseWriter, r *http.Request, what string, done func(SessionStatus) bool, act func(context.Context, string) error) {
	if !s.sessionRuntime(w) {
		return
	}
	id := r.PathValue("id")
	defer s.lockSession(id)()
	rec, ok := s.knownSession(w, id)
	if !ok {
		return
	}
	if err := act(r.Context(), id); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errSessionBusy) {
			code = http.StatusLocked
		}
		http.Error(w, what+" the session: "+err.Error(), code)
		return
	}
	st, err := s.status(r.Context(), rec, true, false)
	if err != nil {
		http.Error(w, "reading the session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !done(st) {
		http.Error(w, what+" the session: Docker still shows it", http.StatusInternalServerError)
		return
	}
	writeJSON(w, st)
}

// removeSession ends a session for good. The record is marked removed
// first, so nothing starts it again; then its runtime and files are taken
// away. The answer is success only once the worker's Docker shows neither
// the container nor the private daemon: until then AppTown keeps holding
// the chat's change, and asks again. A session the worker never knew is
// recorded as removed too, and anything of it found is taken away.
func (s *Server) removeSession(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	id := r.PathValue("id")
	defer s.lockSession(id)()
	rec, ok, err := s.readSession(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		rec = sessionRecord{ID: id, Created: s.now().UTC()}
	}
	if !rec.Removed {
		rec.Removed = true
		if err := s.writeSession(rec); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	st, err := s.takeAway(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, st)
}

// takeAway removes a removed session's runtime and files, and proves the
// runtime is gone by reading it back: a removal that reported success is not
// proof by itself.
func (s *Server) takeAway(ctx context.Context, id string) (SessionStatus, error) {
	if err := s.Sessions.Remove(ctx, id); err != nil {
		return SessionStatus{}, fmt.Errorf("removing the session: %w", err)
	}
	st, err := s.status(ctx, sessionRecord{ID: id, Removed: true}, true, false)
	if err != nil {
		return SessionStatus{}, fmt.Errorf("checking the session is gone: %w", err)
	}
	if !st.Gone() {
		return SessionStatus{}, errors.New("the session's container or private Docker is still present")
	}
	if err := os.RemoveAll(s.sessionFiles(id)); err != nil {
		return SessionStatus{}, fmt.Errorf("removing the session's files: %w", err)
	}
	return st, nil
}

// getSession reports a session: its record and what Docker shows. It does
// not wait for the session's lock: a record is replaced whole, and a report
// is a reading at the time of asking.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	id := r.PathValue("id")
	rec, ok, err := s.readSession(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		rec = sessionRecord{ID: id}
	}
	s.answerSession(w, r.Context(), rec, ok)
}

func (s *Server) answerSession(w http.ResponseWriter, ctx context.Context, rec sessionRecord, known bool) {
	st, err := s.status(ctx, rec, known, !rec.Removed && known)
	if err != nil {
		http.Error(w, "reading the session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, st)
}

func (s *Server) status(ctx context.Context, rec sessionRecord, known, stale bool) (SessionStatus, error) {
	st := SessionStatus{ID: rec.ID, Known: known, Removed: rec.Removed, FilesDigest: rec.FilesDigest, Source: rec.Source}
	cur, err := s.Sessions.Status(ctx, rec.ID)
	if err != nil {
		return st, err
	}
	st.Agent.Exists, st.Agent.Running, st.Agent.EverStarted, st.Agent.Attached = cur.Exists, cur.Running, cur.EverStarted, cur.Attached
	st.Docker.Exists, st.Docker.Running = cur.SidecarExists, cur.SidecarRunning
	st.Script.Exists = cur.ScriptExists
	if stale && cur.Exists && !cur.Running {
		if st.Stale, err = s.Sessions.Stale(ctx, s.launch(rec)); err != nil {
			return st, err
		}
	}
	return st, nil
}

// listSessions lists the worker's session records, removed ones included.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	recs, err := s.sessionRecords()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []SessionSummary{}
	for _, rec := range recs {
		out = append(out, SessionSummary{ID: rec.ID, Created: rec.Created, Removed: rec.Removed})
	}
	writeJSON(w, out)
}

func (s *Server) sessionRecords() ([]sessionRecord, error) {
	entries, err := os.ReadDir(filepath.Join(s.State, "sessions"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []sessionRecord
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !sessionPattern.MatchString(id) {
			continue
		}
		rec, found, err := s.readSession(id)
		if err != nil {
			return nil, fmt.Errorf("session %s: %w", id, err)
		}
		if found {
			out = append(out, rec)
		}
	}
	return out, nil
}

// ReapSessions is run when the worker starts: it finishes removing sessions
// whose removal did not complete, and removes private daemons left by
// sessions it has no live record of. A session that cannot be read is kept.
func (s *Server) ReapSessions(ctx context.Context) error {
	if s.Sessions == nil {
		return nil
	}
	recs, err := s.sessionRecords()
	if err != nil {
		return err
	}
	alive := map[string]bool{}
	for _, rec := range recs {
		if !rec.Removed {
			alive[rec.ID] = true
			continue
		}
		unlock := s.lockSession(rec.ID)
		if _, err := os.Stat(s.sessionFiles(rec.ID)); err == nil {
			if _, err := s.takeAway(ctx, rec.ID); err != nil {
				log.Printf("worker: finishing the removal of session %s: %v", rec.ID, err)
			}
		}
		unlock()
	}
	if err := s.Sessions.Reap(ctx, alive); err != nil {
		return err
	}
	return s.reapBlobs(recs)
}

// reapBlobs removes blobs no live session mounts. An upload in progress
// is under a temporary name and untouched.
func (s *Server) reapBlobs(recs []sessionRecord) error {
	used := map[string]bool{}
	for _, rec := range recs {
		if !rec.Removed {
			for _, m := range rec.Spec.Mounts {
				used[m.Blob] = true
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.State, "blobs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if blobPattern.MatchString(e.Name()) && !used[e.Name()] {
			if err := os.RemoveAll(filepath.Join(s.State, "blobs", e.Name())); err != nil {
				log.Printf("worker: removing blob %s: %v", e.Name(), err)
			}
		}
	}
	return nil
}
