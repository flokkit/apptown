// Package worker is the execution side of a separated AppTown
// (design/operations/workers.md): a small
// program on a machine that shares no files with AppTown, which runs tasks
// on its own Docker daemon for one registered installation.
//
// It knows nothing about projects, workflows or AppTown's database. The
// AppTown reaches it over mutual TLS (a client certificate naming the
// installation it is registered to) and moves source only as Git bundles:
// a task's source arrives as a bundle the worker clones, and its result
// leaves as a bundle of the commits it made.
//
// Before it acts on a task, the worker writes one fsynced record of the
// request under its state directory. A restarted worker answers from those
// records and from Docker, so a repeated request finds the same task.
package worker

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/toolversions"
)

// maxBundleBytes bounds a source or result bundle in either direction.
const maxBundleBytes = 256 << 20

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,127}$`)

// Server serves one installation's tasks.
type Server struct {
	// Installation is the only client identity the worker accepts: the
	// Common Name of the client certificate.
	Installation string
	// State holds records and workspaces; nothing else on the machine is
	// touched.
	State string
	// Tasks runs the tasks: the worker's own Docker daemon in production.
	Tasks execution.TaskDriver
	// Sessions runs chats (sessions.go): AppTown's own chat runtime on the
	// worker's Docker. Nil for a worker that runs none.
	Sessions console.Agent
	// Images answers whether a session's image is on the worker's Docker;
	// a session whose image is absent is refused with the reason. Nil
	// checks nothing.
	Images interface {
		Has(ctx context.Context, image string) (bool, error)
	}
	// Checks runs check containers (checks.go) on the worker's Docker. Nil
	// for a worker that runs none.
	Checks interface {
		Run(ctx context.Context, backend string, req execution.Request) execution.Outcome
	}
	// AgentImage is the agent image this worker built (agent_image.go),
	// reported with its labels, read through ImageLabels, at
	// GET /v1/agent-image. "" reports none.
	AgentImage  string
	ImageLabels interface {
		Labels(ctx context.Context, image string) (map[string]string, error)
	}

	mu sync.Mutex // serializes record writes per worker
	// images: preparing a check image, and recording a use of one, hold it
	// shared; the sweep holds it alone while it chooses and removes, so an
	// image a preparation has just returned is never being removed.
	images sync.RWMutex
	// DindImage is the image of a task's private Docker daemon
	// (private.go); "" refuses a task that asks for one.
	DindImage string
	// ToolVersions names the images the worker runs for itself (a preview's
	// static server, chown and df), one snapshot per preview.
	ToolVersions toolversions.Source
	// Docker is the Docker private Docker runs on and the profile is
	// measured from (docker.CLI in production); nil is the docker CLI.
	Docker docker.Client
	// Profile is what the worker measured when it started
	// (MeasureProfile), reported at GET /v1/profile.
	Profile   execution.WorkerProfile
	taskLocks map[string]*sync.Mutex
	// ServiceLimits are each private service's limits (private.go); zero is
	// execution.DefaultServiceLimits. A check's services get Checks' own.
	ServiceLimits execution.Limits
	// StateDisk reads the filesystem holding a folder: the state folder's,
	// for admitting new work. Nil reads it (diskspace.Statfs).
	StateDisk func(path string) (diskspace.Space, error)
	// ToolCache is the folder mounted writable at execution.ToolCacheMount
	// in every task's and check's container (tool_cache.go); "" mounts none.
	ToolCache string
	// Previews is the Docker previews boot with (docker.CLI in production);
	// nil runs none.
	Previews docker.Client
	// Now is the worker's clock: what it stamps and judges expiries,
	// deadlines and leases by. Nil is the real clock (clock.Or).
	Now          func() time.Time
	previewLocks map[string]*sync.Mutex
	fence        previewFence // previews.go: the fence on requests that change previews
	sessionLocks map[string]*sync.Mutex
}

// now is the worker's clock (Server.Now).
func (s *Server) now() time.Time { return clock.Or(s.Now)() }

// record is what the worker wrote before acting on a task. Workspace names
// the workspace it works in: its own when it is the task's key, which goes
// with the task; otherwise one it shares, which stays until it is released
// itself (DELETE /v1/workspaces/{key}).
type record struct {
	Key       string    `json:"key"`
	Digest    string    `json:"digest"`
	Workspace string    `json:"workspace"`
	Base      string    `json:"base"`
	Created   time.Time `json:"created"`
	// Private names the task's private Docker before any of it exists.
	Private *privateSet `json:"private,omitempty"`
}

// digest identifies what a key was ensured with, so a retry with the same
// key but a different request is refused. The expiry is left out: a retry
// may carry a later one for the same work.
func requestDigest(r execution.WorkerTaskRequest) string {
	if r.Request != "" {
		// The client's identity covers what the task runs, including its
		// source commit; the worker keeps it, so a client can check it.
		return r.Request
	}
	r.Admission.Expires = time.Time{}
	body, _ := json.Marshal(r)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// TLSConfig is the worker's transport: TLS 1.3 only, and every client must
// present a certificate signed by clients. Which installation the
// certificate names is checked per request (Handler).
func TLSConfig(cert tls.Certificate, clients *x509.CertPool) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clients, MinVersion: tls.VersionTLS13}
}

// Handler routes the worker's API. The caller wraps it in a TLS server that
// requires a client certificate; Handler checks the certificate names this
// worker's installation.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/disks", s.disks)
	mux.HandleFunc("POST /v1/checks", s.runCheck)
	mux.HandleFunc("POST /v1/check-images", s.checkImage)
	s.agentRoutes(mux)
	// Its version is open to any caller of this installation, whatever it
	// speaks: it is how an operator and the other side learn what it serves.
	protocols := http.NewServeMux()
	protocols.HandleFunc("GET /v1/version", s.version)
	protocols.Handle("/", served(mux))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != s.Installation {
			http.Error(w, "this worker serves another installation", http.StatusForbidden)
			return
		}
		protocols.ServeHTTP(w, r)
	})
}

// agentRoutes are what a worker serves beside checks.
func (s *Server) agentRoutes(mux *http.ServeMux) {
	// Keys and session ids name files under State. A path value is known
	// only once a route has matched, so each route checks its own.
	task := func(h http.HandlerFunc) http.HandlerFunc { return checked("key", keyPattern, h) }
	session := func(h http.HandlerFunc) http.HandlerFunc { return checked("id", sessionPattern, h) }
	blob := func(h http.HandlerFunc) http.HandlerFunc { return checked("sum", blobPattern, h) }
	mux.HandleFunc("PUT /v1/workspaces/{key}", task(s.putWorkspace))
	mux.HandleFunc("GET /v1/workspaces/{key}/result", task(s.workspaceResult))
	mux.HandleFunc("DELETE /v1/workspaces/{key}", task(s.deleteWorkspace))
	mux.HandleFunc("POST /v1/tasks/{key}", task(s.ensure))
	mux.HandleFunc("POST /v1/tasks/{key}/start", task(s.start))
	mux.HandleFunc("GET /v1/tasks/{key}", task(s.observe))
	mux.HandleFunc("POST /v1/tasks/{key}/wait", task(s.wait))
	mux.HandleFunc("GET /v1/tasks/{key}/output", task(s.output))
	mux.HandleFunc("POST /v1/tasks/{key}/stop", task(s.stop))
	mux.HandleFunc("DELETE /v1/tasks/{key}", task(s.release))
	mux.HandleFunc("GET /v1/tasks/{key}/results", task(s.results))
	mux.HandleFunc("GET /v1/tasks/{key}/images", task(s.taskImages))
	mux.HandleFunc("GET /v1/tasks/{key}/images/{name}/{file...}", task(s.imageFile))
	mux.HandleFunc("GET /v1/agent-image", s.agentImage)
	mux.HandleFunc("GET /v1/profile", s.profile)
	mux.HandleFunc("GET /v1/sessions", s.listSessions)
	mux.HandleFunc("PUT /v1/sessions/{id}", session(s.putSession))
	mux.HandleFunc("GET /v1/sessions/{id}", session(s.getSession))
	mux.HandleFunc("POST /v1/sessions/{id}/start", session(s.startSession))
	mux.HandleFunc("POST /v1/sessions/{id}/stop", session(s.stopSession))
	mux.HandleFunc("POST /v1/sessions/{id}/rebuild", session(s.rebuildSession))
	mux.HandleFunc("DELETE /v1/sessions/{id}", session(s.removeSession))
	mux.HandleFunc("PUT /v1/sessions/{id}/files", session(s.putFiles))
	mux.HandleFunc("PUT /v1/sessions/{id}/source", session(s.putSource))
	mux.HandleFunc("GET /v1/sessions/{id}/save", session(s.save))
	mux.HandleFunc("POST /v1/sessions/{id}/deliver", session(s.deliver))
	mux.HandleFunc("POST /v1/sessions/{id}/interrupt", session(s.interrupt))
	mux.HandleFunc("GET /v1/sessions/{id}/terminal", session(s.terminal))
	mux.HandleFunc("GET /v1/sessions/{id}/records", session(s.records))
	mux.HandleFunc("GET /v1/sessions/{id}/records/{name}", session(s.record))
	mux.HandleFunc("POST /v1/reach", s.reach)
	mux.HandleFunc("PUT /v1/previews/{name}", s.bootPreview)
	mux.HandleFunc("POST /v1/preview-admission", s.admitPreview)
	mux.HandleFunc("GET /v1/previews/{name}", s.getPreview)
	mux.HandleFunc("DELETE /v1/previews/{name}", s.deletePreview)
	mux.HandleFunc("DELETE /v1/previews/{name}/images", s.deletePreviewImages)
	mux.HandleFunc("GET /v1/previews", s.listPreviews)
	mux.HandleFunc("DELETE /v1/previews", s.deletePreviews)
	mux.HandleFunc("/v1/previews/{name}/http", s.forwardPreview)
	mux.HandleFunc("POST /v1/inputs/check", s.checkInputSet)
	mux.HandleFunc("HEAD /v1/blobs/{sum}", blob(s.headBlob))
	mux.HandleFunc("PUT /v1/blobs/{sum}", blob(s.putBlob))
}

// checked refuses a request whose path value name does not match pattern.
func checked(name string, pattern *regexp.Regexp, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !pattern.MatchString(r.PathValue(name)) {
			http.Error(w, "invalid "+name, http.StatusBadRequest)
			return
		}
		h(w, r)
	}
}

func (s *Server) recordPath(key string) string { return filepath.Join(s.State, "records", key+".json") }

// releasedPath marks a key whose task was released.
func (s *Server) releasedPath(key string) string { return filepath.Join(s.State, "released", key) }

// releasing reports whether a key's release was decided: it may still be
// being removed, and nothing of it starts.
func (s *Server) releasing(key string) bool {
	_, err := os.Stat(s.releasedPath(key))
	return err == nil
}

// abandonAt is called between each durable decision about a task's
// private Docker and the Docker effect that follows it; a test makes it
// abandon the request there, as a worker stopped at that moment would
// (export_test.go). The points: "recorded" (ensure: record → network),
// "network" (start: network → services), "services" (services → daemon),
// "exited" (exit → stop), "quiet" (stop recorded → capture) and
// "releasing" (release decided → removal).
var abandonAt = func(point, key string) {}

// A workspace is a work tree the task sees at /work, and a Git directory
// kept apart from it. The task never sees the Git directory, so nothing it
// writes can become configuration or hooks for the worker's own git (which
// is also run with hooks off). /work has no .git unless the upload asks for
// the agent's own repository there (agent_git.go), which the worker's git
// never reads.
func (s *Server) workspacePath(key string) string { return filepath.Join(s.State, "workspaces", key) }
func (s *Server) gitDir(key string) string        { return filepath.Join(s.State, "git", key+".git") }

// sourceRef names, in a workspace's Git directory, the commit it was
// uploaded with.
const sourceRef = "refs/apptown/source"

// writeRecord makes the record durable before the worker acts: written to a
// temporary file, fsynced, renamed, and the directory fsynced.
func (s *Server) writeRecord(rec record) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeDurable(filepath.Join(s.State, "records"), rec.Key+".json", body)
}

// writeDurable makes dir (0700) and writes dir/name with durable.Write.
// afterRename is where a test injects a failure after a durable write has
// made the new file visible (export_test.go); nil otherwise.
var afterRename = func(dir, name string) error { return nil }

func writeDurable(dir, name string, body []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := durable.Write(dir, name, body, 0o600, false); err != nil {
		return err
	}
	return afterRename(dir, name)
}

func (s *Server) readRecord(key string) (record, bool, error) {
	body, err := os.ReadFile(s.recordPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, err
	}
	var rec record
	return rec, true, json.Unmarshal(body, &rec)
}

// putWorkspace makes a workspace from an uploaded bundle holding the given
// commit as ref. Repeating the upload of the same commit is accepted and
// changes nothing; another commit under the same key is a conflict.
func (s *Server) putWorkspace(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	ref, commit := r.URL.Query().Get("ref"), r.URL.Query().Get("commit")
	if !strings.HasPrefix(ref, "refs/heads/") || len(commit) != 40 {
		http.Error(w, "a workspace needs the branch ref to check out and its commit", http.StatusBadRequest)
		return
	}
	agentGit, err := s.agentGitFrom(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A new workspace is the first thing a new task writes: refused, before
	// its bundle is read, on a disk short of space (admitNew).
	if _, err := os.Stat(s.workspacePath(key)); errors.Is(err, fs.ErrNotExist) {
		if err := s.admitNew(r.Context()); err != nil {
			execution.WriteRefusal(w, http.StatusUnprocessableEntity, err)
			return
		}
	}
	incoming := filepath.Join(s.State, "incoming")
	if err := os.MkdirAll(incoming, 0o700); err != nil {
		httpError(w, err)
		return
	}
	bundle, err := os.CreateTemp(incoming, key+".*.bundle")
	if err != nil {
		httpError(w, err)
		return
	}
	defer os.Remove(bundle.Name())
	n, err := io.Copy(bundle, io.LimitReader(r.Body, maxBundleBytes+1))
	bundle.Close()
	if err != nil {
		httpError(w, err)
		return
	}
	if n > maxBundleBytes {
		http.Error(w, "the bundle is larger than the worker accepts", http.StatusRequestEntityTooLarge)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, err := s.git(r.Context(), key, "rev-parse", "--verify", "-q", sourceRef); err == nil {
		if existing != commit {
			execution.WriteRefusal(w, http.StatusConflict, fmt.Errorf("%w: this workspace was uploaded with another commit", execution.ErrTaskConflict))
			return
		}
		writeJSON(w, map[string]string{"workspace": key})
		return
	}
	staging, err := os.MkdirTemp(incoming, key+".*")
	if err != nil {
		httpError(w, err)
		return
	}
	defer os.RemoveAll(staging)
	gitDir, tree := filepath.Join(staging, "git"), filepath.Join(staging, "tree")
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if out, err := s.runGit(r.Context(), "", "clone", "-q", "--bare", "--branch", branch, bundle.Name(), gitDir); err != nil {
		http.Error(w, "cloning the bundle: "+out, http.StatusUnprocessableEntity)
		return
	}
	if head, err := s.runGit(r.Context(), "", "--git-dir", gitDir, "rev-parse", "--verify", ref+"^{commit}"); err != nil || head != commit {
		http.Error(w, "the bundle does not hold the named commit", http.StatusUnprocessableEntity)
		return
	}
	// A workspace has no remote: results leave only as bundles.
	_, _ = s.runGit(r.Context(), "", "--git-dir", gitDir, "remote", "remove", "origin")
	// AppTown's runtime files under .apptown (a step's report, its agent's
	// home) are not the task's work, whatever the project's own ignore rules
	// say: a repository started from existing code need not ignore them.
	// Git's own exclude file, apart from the tree, keeps them out of what is
	// collected; AppTown still refuses any that arrive.
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o700); err != nil {
		httpError(w, err)
		return
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "exclude"), []byte(".apptown/*\n!.apptown/app.yaml\n"), 0o600); err != nil {
		httpError(w, err)
		return
	}
	if err := os.Mkdir(tree, 0o755); err != nil {
		httpError(w, err)
		return
	}
	for _, args := range [][]string{{"checkout", "-q", "-f", branch}, {"update-ref", sourceRef, commit}} {
		if out, err := s.runGit(r.Context(), "", append([]string{"--git-dir", gitDir, "--work-tree", tree, "-c", "core.bare=false"}, args...)...); err != nil {
			http.Error(w, "preparing the workspace: "+out, http.StatusInternalServerError)
			return
		}
	}
	if agentGit != nil {
		if out, err := s.seedAgentGit(r.Context(), bundle.Name(), branch, commit, tree, staging, agentGit); err != nil {
			http.Error(w, "making the agent's repository: "+out, http.StatusUnprocessableEntity)
			return
		}
	}
	// The Git directory is moved last: its presence marks a complete
	// workspace, so a crash between the moves leaves one that is redone.
	for _, dir := range []string{filepath.Dir(s.workspacePath(key)), filepath.Dir(s.gitDir(key))} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			httpError(w, err)
			return
		}
	}
	_ = os.RemoveAll(s.workspacePath(key))
	if err := os.Rename(tree, s.workspacePath(key)); err != nil {
		httpError(w, err)
		return
	}
	if err := os.Rename(gitDir, s.gitDir(key)); err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, map[string]string{"workspace": key})
}

// workspaceResult commits whatever the task left in its workspace and
// returns the commits after the workspace's source as a bundle. base must be
// that source. With nothing new it answers 204.
func (s *Server) workspaceResult(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	base, message := r.URL.Query().Get("base"), r.URL.Query().Get("message")
	if base == "" || message == "" {
		http.Error(w, "a result needs its base commit and commit message", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	source, err := s.git(r.Context(), key, "rev-parse", "--verify", "-q", sourceRef)
	if err != nil {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	if source != base {
		http.Error(w, "this workspace was given another commit", http.StatusConflict)
		return
	}
	// Nothing is taken while a private daemon or service that can write the
	// tree is not confirmed stopped: what is collected must not change.
	if writers, err := s.privateWriters(key); err != nil || len(writers) > 0 {
		if err != nil {
			httpError(w, err)
			return
		}
		http.Error(w, "the workspace's private Docker is not confirmed stopped: "+strings.Join(writers, ", "), http.StatusConflict)
		return
	}
	// The worker returns what the task changed, as git takes it: the
	// project's ignore rules and the workspace's exclude file (putWorkspace)
	// apply, and the native app contract is taken even where they ignore
	// it. Which of that may enter the change's branch is AppTown's
	// decision where it accepts the result (scm's publication check), not
	// the worker's.
	if out, err := s.git(r.Context(), key, "add", "-A", "--", "."); err != nil {
		httpError(w, fmt.Errorf("staging the task's changes: %w: %s", err, out))
		return
	}
	contract := filepath.Join(s.workspacePath(key), ".apptown", "app.yaml")
	tracked, err := s.git(r.Context(), key, "ls-files", "--", ".apptown/app.yaml")
	if err != nil {
		httpError(w, err)
		return
	}
	if _, statErr := os.Lstat(contract); statErr == nil || tracked != "" {
		if _, err := s.git(r.Context(), key, "add", "-A", "-f", "--", ".apptown/app.yaml"); err != nil {
			httpError(w, err)
			return
		}
	}
	// A commit is made only for staged changes: ignored files do not call
	// for one, and a result already committed (a retried collection) is
	// bundled again as it is.
	staged, err := s.git(r.Context(), key, "diff", "--cached", "--name-only")
	if err != nil {
		httpError(w, err)
		return
	}
	if staged != "" {
		if out, err := s.git(r.Context(), key, "-c", "user.name=apptown", "-c", "user.email=apptown@localhost", "commit", "-q", "-m", message); err != nil {
			http.Error(w, "committing the task's work: "+out, http.StatusInternalServerError)
			return
		}
	}
	head, err := s.git(r.Context(), key, "rev-parse", "HEAD")
	if err != nil {
		httpError(w, err)
		return
	}
	if head == base {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	branch, err := s.git(r.Context(), key, "symbolic-ref", "HEAD")
	if err != nil {
		httpError(w, err)
		return
	}
	out, err := os.CreateTemp(filepath.Join(s.State, "incoming"), "result.*.bundle")
	if err != nil {
		httpError(w, err)
		return
	}
	out.Close()
	defer os.Remove(out.Name())
	if msg, err := s.git(r.Context(), key, "bundle", "create", "-q", out.Name(), base+".."+branch); err != nil {
		http.Error(w, "bundling the task's commits: "+msg, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("AppTown-Head", head)
	f, err := os.Open(out.Name())
	if err != nil {
		httpError(w, err)
		return
	}
	defer f.Close()
	_, _ = io.Copy(w, f)
}

func (s *Server) ensure(w http.ResponseWriter, r *http.Request) {
	var req execution.WorkerTaskRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil || req.Key != r.PathValue("key") {
		http.Error(w, "an invalid task request", http.StatusBadRequest)
		return
	}
	// Under the task's lock, as start, stop and release are: an ensure
	// never makes the task while a release removes it.
	unlock := s.lockTask(req.Key)
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, found, err := s.readRecord(req.Key)
	if err != nil {
		httpError(w, err)
		return
	}
	if found && existing.Digest != requestDigest(req) {
		execution.WriteRefusal(w, http.StatusConflict, fmt.Errorf("%w: this key was ensured with a different request", execution.ErrTaskConflict))
		return
	}
	if s.releasing(req.Key) {
		http.Error(w, "this task was released: a request for it is stale", http.StatusGone)
		return
	}
	spec := execution.TaskSpec{Key: req.Key, Admission: req.Admission, Image: req.Image, Argv: req.Argv, Env: req.Env, Labels: req.Labels, WorkspaceRO: req.ReadOnly, Request: requestDigest(req), Private: req.Private}
	if err := spec.Validate(s.now()); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, execution.ErrExpired) || errors.Is(err, execution.ErrUnsupported) {
			status = http.StatusUnprocessableEntity
		}
		execution.WriteRefusal(w, status, err)
		return
	}
	if err := execution.ValidResults(req.Results); err != nil {
		http.Error(w, "the task's result files: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := execution.ValidImages(req.Images); err != nil {
		http.Error(w, "the task's images: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Workspace != "" {
		if !keyPattern.MatchString(req.Workspace) {
			http.Error(w, "invalid workspace", http.StatusBadRequest)
			return
		}
		if source, err := s.git(r.Context(), req.Workspace, "rev-parse", "--verify", "-q", sourceRef); err != nil || source != req.Source {
			http.Error(w, "the task's workspace was not uploaded with its source commit", http.StatusUnprocessableEntity)
			return
		}
		spec.Workspace = s.workspacePath(req.Workspace)
	}
	if err := execution.ValidFiles(req.Files); err != nil {
		http.Error(w, "the task's generated files: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Inputs and results are refused, with nothing created, when they are
	// invalid, over the bounds, or name a blob this worker lacks.
	if err := s.checkInputs(req.Inputs); err != nil {
		refuseInputs(w, err)
		return
	}
	// A task without a workspace leaves its result files in a state folder
	// of its own (taskStatePath), so they can only be there; its images
	// need a workspace.
	if len(req.Images) > 0 && req.Workspace == "" {
		http.Error(w, "the task's images: a task without a workspace has nowhere to leave them", http.StatusBadRequest)
		return
	}
	ownState := len(req.Results) > 0 && req.Workspace == ""
	if ownState {
		for _, res := range req.Results {
			if !strings.HasPrefix(res.Path, execution.StateRoot+"/") {
				http.Error(w, "the task's result files: a task without a workspace leaves them under "+execution.StateRoot+", not at "+res.Path, http.StatusBadRequest)
				return
			}
		}
	}
	switch {
	case req.Private != nil && req.Workspace == "":
		http.Error(w, "private Docker: a task without a workspace has no /work to share with it", http.StatusBadRequest)
		return
	case req.Private != nil && req.Private.Daemon && s.DindImage == "":
		execution.WriteRefusal(w, http.StatusUnprocessableEntity, fmt.Errorf("%w: this worker runs no private Docker daemon", execution.ErrUnsupported))
		return
	}
	// A new task's private Docker is refused, before anything is made, when
	// it was admitted on another profile than this worker's now.
	if req.Private != nil && !found {
		if err := s.sameProfile(r.Context(), req.Private.Profile, req.Private.Daemon); errors.Is(err, errProfileChanged) {
			execution.WriteRefusal(w, http.StatusUnprocessableEntity, fmt.Errorf("%w: %w", execution.ErrUnsupported, err))
			return
		} else if err != nil {
			httpError(w, err)
			return
		}
	}
	// A new task is admitted once, before the first thing of it is written
	// (admitNew): a task in its own workspace was admitted when that was
	// uploaded; any other is admitted here, before its record.
	if !found && req.Workspace != req.Key {
		if err := s.admitNew(r.Context()); err != nil {
			execution.WriteRefusal(w, http.StatusUnprocessableEntity, err)
			return
		}
	}
	if !found {
		existing = record{Key: req.Key, Digest: requestDigest(req), Workspace: req.Workspace, Base: req.Source, Created: s.now().UTC()}
		if req.Private != nil {
			existing.Private = s.privateSetFor(req.Key, req)
		}
		if err := s.writeRecord(existing); err != nil {
			httpError(w, err)
			return
		}
		abandonAt("recorded", req.Key)
	}
	// The task joins its private network, made now, before its container;
	// the rest is made when it starts. Private Docker is the worker's, not
	// its task driver's.
	if p := existing.Private; p != nil {
		if err := s.makeNetwork(r.Context(), req.Key, p); err != nil {
			httpError(w, err)
			return
		}
		spec.Private, spec.Network = nil, p.Network
		if p.Spec.Daemon {
			spec.Env = append(slices.Clone(spec.Env), "DOCKER_HOST=tcp://dind:2375", "DOCKER_CONFIG="+dockerClient, "BUILDX_BUILDER=apptown")
		}
	}
	// The files are kept in the task's own folder from before it is created
	// until it is released; an ensure repeated with other files is refused,
	// and files a task may have mounted are never rewritten.
	mounts, err := execution.StageFiles(s.taskFilesPath(req.Key), req.Files)
	if errors.Is(err, execution.ErrTaskConflict) {
		execution.WriteRefusal(w, http.StatusConflict, fmt.Errorf("this key was ensured with other generated files: %w", err))
		return
	}
	if err != nil {
		httpError(w, err)
		return
	}
	spec.Mounts = mounts
	inputs, err := s.stageInputs(req.Key, req.Inputs)
	if errors.Is(err, execution.ErrTaskConflict) {
		execution.WriteRefusal(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		httpError(w, err)
		return
	}
	spec.Mounts = append(spec.Mounts, inputs...)
	if req.Workspace != "" {
		// The workspace's state, beside its tree, for every task in it.
		if err := os.MkdirAll(s.statePath(req.Workspace), 0o755); err != nil {
			httpError(w, err)
			return
		}
		spec.Mounts = append(spec.Mounts, s.statePath(req.Workspace)+":"+execution.StateRoot)
	}
	declared := declaredResults{Workspace: req.Workspace, Results: req.Results, Images: req.Images}
	if ownState {
		declared.State = s.taskStatePath(req.Key)
		if err := os.MkdirAll(declared.State, 0o755); err != nil {
			httpError(w, err)
			return
		}
		spec.Mounts = append(spec.Mounts, declared.State+":"+execution.StateRoot)
	}
	spec.Mounts = append(spec.Mounts, s.toolCacheMounts()...)
	if err := s.writeDeclared(req.Key, declared); err != nil {
		httpError(w, err)
		return
	}
	obs, err := s.Tasks.Ensure(r.Context(), spec)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, obs)
}

// disks is GET /v1/disks: each filesystem this worker writes to, read now
// (diskspace.Reading): its state folder, and the Docker its tasks run on
// (their containers, images and private Docker), as its task driver reads
// it. A driver that reads no Docker disk reports none.
func (s *Server) disks(w http.ResponseWriter, r *http.Request) {
	space, err := s.stateDisk()
	out := []diskspace.Reading{diskspace.NewReading("the worker's state folder", s.State, space, err, s.now())}
	if d, ok := s.Tasks.(dataDisk); ok {
		root, space, err := d.DataDisk(r.Context())
		out = append(out, diskspace.NewReading("the worker's Docker data (tasks, images, private Docker)", root, space, err, s.now()))
	}
	writeJSON(w, out)
}

// admitNew says whether new work may start here (diskspace.Admit), both
// disks read now: the state folder (workspaces, files, inputs, results)
// and, where the task driver reads one, the Docker data folder its tasks'
// containers, images and private Docker land in. It runs once for a new
// task, before the first thing of it is written, so its refusal leaves
// nothing. A disk filling after it is the accepted race: the work then
// fails as it would without the check (execution.DockerTasks on a worker
// does not read the disk again: CallerAdmitsDisk).
func (s *Server) admitNew(ctx context.Context) error {
	space, err := s.stateDisk()
	if err := diskspace.Admit("the worker's state folder ("+s.State+")", space, err); err != nil {
		return err
	}
	if d, ok := s.Tasks.(dataDisk); ok {
		root, space, err := d.DataDisk(ctx)
		refusal := diskspace.Admit("the worker's Docker data folder ("+root+")", space, err)
		if refusal != nil {
			// The refusal goes to the caller; the log keeps it here too,
			// with what the disk read showed, should the caller's copy be lost.
			log.Printf("worker: refusing new work: %v", refusal)
		}
		return refusal
	}
	return nil
}

// dataDisk is a task driver that reads the Docker data folder its tasks
// use (execution.DockerTasks).
type dataDisk interface {
	DataDisk(context.Context) (string, diskspace.Space, error)
}

// stateDisk reads the filesystem of the state folder (Server.StateDisk).
func (s *Server) stateDisk() (diskspace.Space, error) {
	if s.StateDisk != nil {
		return s.StateDisk(s.State)
	}
	return diskspace.Statfs(s.State)
}

// taskFilesPath is the folder a task's generated files are kept in.
func (s *Server) taskFilesPath(key string) string { return filepath.Join(s.State, "task-files", key) }

func (s *Server) ref(r *http.Request) execution.TaskRef {
	return execution.TaskRef{Key: r.PathValue("key"), ProviderID: r.URL.Query().Get("id")}
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	unlock := s.lockTask(key)
	defer unlock()
	rec, found, err := s.readRecord(key)
	if err == nil && !found {
		http.Error(w, "no such task: it was never ensured, or was released", http.StatusNotFound)
		return
	}
	if s.releasing(key) {
		http.Error(w, "this task is being released: a request for it is stale", http.StatusGone)
		return
	}
	if err == nil && rec.Private != nil {
		err = s.startPrivate(r.Context(), rec)
		if errors.Is(err, execution.ErrStartRefused) {
			execution.WriteRefusal(w, http.StatusUnprocessableEntity, err)
			return
		}
	}
	if err != nil {
		httpError(w, err)
		return
	}
	d, _, err := s.readDeclared(key)
	if err != nil {
		httpError(w, err)
		return
	}
	if d.hasFresh() {
		// Removing the Fresh results and starting are one step: a start
		// repeated meanwhile finds the task started and removes nothing.
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.freshen(r.Context(), s.ref(r), d); err != nil {
			httpError(w, err)
			return
		}
	}
	if err := s.Tasks.Start(r.Context(), s.ref(r)); err != nil {
		httpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) observe(w http.ResponseWriter, r *http.Request) {
	obs, err := s.observeTask(r.Context(), s.ref(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if obs.State == execution.TaskExited {
		s.captureAfterExit(r.Context(), r.PathValue("key"))
	}
	writeJSON(w, obs)
}

// waitSlice bounds one wait request: a task that is still running after it
// answers "running", and the client asks again. No request outlives a
// proxy's idle timeout, and a lost connection loses nothing.
const waitSlice = 25 * time.Second

func (s *Server) wait(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), waitSlice)
	defer cancel()
	code, err := s.Tasks.Wait(ctx, s.ref(r))
	// The slice ending is "running", however the driver reports it (Docker's
	// wait is killed: "signal: killed").
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil {
		writeJSON(w, map[string]any{"running": true})
		return
	}
	if err != nil {
		httpError(w, err)
		return
	}
	if err := s.quiet(ctx, r.PathValue("key")); err != nil {
		// Not exited until everything beside it has stopped: asked again.
		log.Printf("worker: task %s exited; stopping its private Docker: %v", r.PathValue("key"), err)
		// A beat before the client asks again; none for a client gone.
		_ = poll.Wait(r.Context(), time.Second)
		writeJSON(w, map[string]any{"running": true})
		return
	}
	s.captureAfterExit(r.Context(), r.PathValue("key"))
	writeJSON(w, map[string]any{"exitCode": code})
}

func (s *Server) output(w http.ResponseWriter, r *http.Request) {
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	max, _ := strconv.Atoi(r.URL.Query().Get("max"))
	page, err := s.Tasks.Output(r.Context(), s.ref(r), execution.OutputRead{Cursor: cursor, Max: max})
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, page)
}

// stop stops the whole invocation: the task's container, then its private
// daemon and services, each confirmed stopped. A private stop not confirmed
// is a failed stop: AppTown holds the task (E03), and its workspace is
// not collected (workspaceResult).
func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	unlock := s.lockTask(key)
	defer unlock()
	if err := s.Tasks.Stop(r.Context(), s.ref(r)); err != nil {
		httpError(w, err)
		return
	}
	if err := s.quietLocked(r.Context(), key); err != nil {
		httpError(w, fmt.Errorf("the task stopped; its private Docker was not confirmed stopped: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// release removes the task's container, its own workspace (not one it
// shares) and its record. The decision comes first: the released mark is
// written before anything is removed, so from then on, after a restart
// too, a request for the key is stale and nothing of it is made again;
// the record stays until everything is confirmed gone, and a release
// interrupted or failed is asked again.
func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	unlock := s.lockTask(key)
	defer unlock()
	rec, found, err := s.readRecord(key)
	if err != nil {
		httpError(w, err)
		return
	}
	if found {
		if err := writeDurable(filepath.Dir(s.releasedPath(key)), key, nil); err != nil {
			httpError(w, err)
			return
		}
	}
	abandonAt("releasing", key)
	err = s.Tasks.Release(r.Context(), s.ref(r))
	if err == nil && found && rec.Private != nil {
		// Not confirmed removed: the record stays, and the release is
		// asked again.
		err = s.removePrivate(r.Context(), key, rec.Private)
	}
	if err == nil {
		var obs execution.TaskObservation
		if obs, err = s.Tasks.Observe(r.Context(), execution.TaskRef{Key: key}); err == nil && obs.State != execution.TaskAbsent {
			err = fmt.Errorf("the task's container is still there (%s)", obs.State)
		}
	}
	if err != nil {
		httpError(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if found && rec.Workspace == key {
		for _, dir := range []string{s.gitDir(rec.Workspace), s.workspacePath(rec.Workspace), s.statePath(rec.Workspace)} {
			if err := os.RemoveAll(dir); err != nil {
				httpError(w, err)
				return
			}
		}
	}
	// The generated files, inputs, captured results and images, and the own
	// state of a task without a workspace, go with the task, after its
	// container. An image file a read already has open stays readable until
	// that read ends; a new open is refused as gone.
	for _, dir := range []string{s.taskFilesPath(key), s.taskInputsPath(key), s.taskResultsPath(key), s.taskStatePath(key)} {
		if err := os.RemoveAll(dir); err != nil {
			httpError(w, err)
			return
		}
	}
	if err := os.Remove(s.recordPath(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		httpError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteWorkspace removes a workspace and its Git directory. It is refused
// while any task naming it is not released (its record remains), and a
// workspace already gone is removed.
func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	s.mu.Lock()
	defer s.mu.Unlock()
	holders, err := s.tasksIn(key)
	if err != nil {
		httpError(w, err)
		return
	}
	if len(holders) > 0 {
		http.Error(w, "the workspace still has tasks not released: "+strings.Join(holders, ", "), http.StatusConflict)
		return
	}
	// The Git directory goes first: its presence marks a complete workspace.
	for _, dir := range []string{s.gitDir(key), s.workspacePath(key), s.statePath(key)} {
		if err := os.RemoveAll(dir); err != nil {
			httpError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// tasksIn lists the recorded (not released) tasks working in a workspace.
// The caller holds s.mu.
func (s *Server) tasksIn(workspace string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.State, "records"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !keyPattern.MatchString(key) {
			continue
		}
		rec, found, err := s.readRecord(key)
		if err != nil {
			return nil, fmt.Errorf("reading task %s's record: %w", key, err)
		}
		if found && rec.Workspace == workspace {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// runGit runs git in dir. Nothing from the machine's Git configuration
// applies to task source, no hook or file-system monitor runs, and the
// worker's own state directories are trusted whoever owns their files (a
// task may run as another user).
// gitEnv and gitConfig keep the worker's git from reading any hooks or
// monitors but its own, or prompting; gitexec already keeps it from any
// configuration but the repository's.
var (
	gitEnv    = []string{"GIT_TERMINAL_PROMPT=0"}
	gitConfig = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "safe.directory=*"}
)

func (s *Server) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return gitexec.RunEnv(ctx, dir, gitEnv, append(slices.Clone(gitConfig), args...)...)
}

// git runs git on a workspace: its Git directory and its work tree.
func (s *Server) git(ctx context.Context, key string, args ...string) (string, error) {
	return s.runGit(ctx, "", s.workspaceArgs(key, args)...)
}

func (s *Server) workspaceArgs(key string, args []string) []string {
	return append([]string{"--git-dir", s.gitDir(key), "--work-tree", s.workspacePath(key), "-c", "core.bare=false"}, args...)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, fmt.Sprint(err), http.StatusInternalServerError)
}

// toolVersions is the current tool versions snapshot, or an empty one,
// whose missing images the work that needs them reports.
func (s *Server) toolVersions() toolversions.Snapshot {
	if s.ToolVersions == nil {
		return toolversions.Snapshot{}
	}
	return s.ToolVersions.Current()
}
