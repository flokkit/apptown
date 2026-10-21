package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/gitexec"
)

// A check runs on the worker as one synchronous request, not a recorded
// task (design/operations/workers.md, "Checks on the worker"):
// AppTown sends a staged tree, a spec and, for a history scan, a bundle of
// the commits it reads; the worker runs one container on a scratch copy and
// answers with the exit, the output and the one declared report. It keeps
// nothing: the scratch folder goes with the answer, and anything left over
// (a worker that stopped mid-check) is removed at its deadline by the sweep,
// or at the worker's next start.

const (
	maxCheckTree    = 2 << 30 // bytes of files in one check's tree
	maxCheckEntries = 200000
	maxCheckSpec    = 4 << 20 // the spec, with its generated files (execution.MaxFileBytes, as base64)
	maxCheckHistory = 2 << 30
	maxCheckOutput  = 1 << 20
	// checkGrace is the time a check's tree and history take to arrive and
	// unpack, added to its own timeout for its deadline.
	checkGrace = 5 * time.Minute
)

// CheckSpec is the wire form of one check container: the runner's request
// with no path of AppTown's in it.
type CheckSpec struct {
	Image        string   `json:"image"`
	Argv         []string `json:"argv"`
	Env          []string `json:"env,omitempty"`
	NoEntrypoint bool     `json:"noEntrypoint,omitempty"`
	// Egress lets the container reach the network, where a scanner binding
	// asks for it; otherwise it has none.
	Egress bool `json:"egress,omitempty"`
	// Limits bound the check's container (and so a test run in it); a
	// request with none (an older forge) runs under the task defaults.
	Limits         execution.Limits `json:"limits,omitzero"`
	TimeoutSeconds int              `json:"timeoutSeconds"`
	Writable       bool             `json:"writable,omitempty"`
	Report         string           `json:"report,omitempty"`
	// History says a bundle follows the tree, to present read-only at
	// /history.
	History bool `json:"history,omitempty"`
	// ExportImage is an image on this worker (by ID) saved into the tree
	// before the check runs (an image scan).
	ExportImage string `json:"exportImage,omitempty"`
	// NoTree runs the command with nothing mounted, in the image's own
	// working directory (an exec-image check).
	NoTree bool `json:"noTree,omitempty"`
	// Files are generated files the check sees read-only (execution.File),
	// written beside its tree, in the check's folder.
	Files []execution.File `json:"files,omitempty"`
	// Services run beside the check on a network of its own
	// (execution.Request.Services), labelled as the check is, so the sweep
	// removes them with it.
	Services []deploy.ServiceSpec `json:"services,omitempty"`
}

// CheckAnswer is what a check's run answered.
type CheckAnswer struct {
	Termination execution.Termination `json:"termination"`
	ExitCode    int                   `json:"exitCode"`
	RunErr      string                `json:"runErr,omitempty"`
	Output      []byte                `json:"output,omitempty"`
	Report      []byte                `json:"report,omitempty"`
	ReportErr   string                `json:"reportErr,omitempty"`
}

func (s *Server) checksRoot() string { return filepath.Join(s.State, "checks") }

// runCheck is POST /v1/checks: a multipart body of the spec, the tree (a
// tar) and, when the spec asks, the history bundle, in that order.
func (s *Server) runCheck(w http.ResponseWriter, r *http.Request) {
	if s.Checks == nil {
		http.Error(w, "this worker runs no checks", http.StatusNotImplemented)
		return
	}
	ctx, lifetime := bounded(w, r, checkGrace)
	defer lifetime.Stop()
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "a check is a multipart request: "+err.Error(), http.StatusBadRequest)
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "spec" {
		http.Error(w, "a check starts with its spec", http.StatusBadRequest)
		return
	}
	var spec CheckSpec
	if err := json.NewDecoder(io.LimitReader(part, maxCheckSpec)).Decode(&spec); err != nil {
		http.Error(w, "the check's spec: "+err.Error(), http.StatusBadRequest)
		return
	}
	if spec.Image == "" || len(spec.Argv) == 0 || spec.TimeoutSeconds <= 0 {
		http.Error(w, "a check needs an image, a command and a timeout", http.StatusBadRequest)
		return
	}
	// An image that reads as one of Docker's options would widen what the
	// check may do; it is refused here, whatever AppTown sent.
	if !execution.ValidImage(spec.Image) || spec.ExportImage != "" && !execution.ValidImage(spec.ExportImage) {
		http.Error(w, "the check's image is not an image reference", http.StatusBadRequest)
		return
	}
	if err := execution.ValidFiles(spec.Files); err != nil {
		http.Error(w, "the check's generated files: "+err.Error(), http.StatusBadRequest)
		return
	}
	if spec.Report != "" && !localName(spec.Report) {
		http.Error(w, "the report must be a path inside the check's tree", http.StatusBadRequest)
		return
	}
	timeout := time.Duration(spec.TimeoutSeconds) * time.Second
	lifetime.For(timeout + checkGrace)
	// The deadline the sweep judges the check's folder and container by,
	// on the worker's clock (SweepChecks).
	deadline := s.now().Add(timeout + checkGrace)
	id, err := checkID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dir := filepath.Join(s.checksRoot(), fmt.Sprintf("%d-%s", deadline.Unix(), id))
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("check %s: removing its folder: %v (the sweep retries)", id, err)
		}
	}()
	if part, err = mr.NextPart(); err != nil || part.FormName() != "tree" {
		http.Error(w, "the check's tree follows its spec", http.StatusBadRequest)
		return
	}
	tree := filepath.Join(dir, "tree")
	root, err := os.OpenRoot(tree)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = extractBounded(part, root, true, maxCheckTree, maxCheckEntries)
	root.Close()
	if err != nil {
		http.Error(w, "the check's tree: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var history *execution.History
	if spec.History {
		repo, err := receiveHistory(ctx, mr, dir)
		if err != nil {
			http.Error(w, "the check's history: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		history = &execution.History{Repository: repo}
	}
	// The whole body is read before the check runs: only then does the
	// server notice AppTown going away, and cancel the check with it.
	if err := drained(mr, r.Body); err != nil {
		http.Error(w, "the check's request: "+err.Error(), http.StatusBadRequest)
		return
	}
	network := "none"
	if spec.Egress {
		network = ""
	}
	s.images.RLock()
	s.usedImage(spec.Image)
	s.usedImage(spec.ExportImage)
	s.images.RUnlock()
	workspace := tree
	if spec.NoTree {
		workspace = ""
	}
	limits := spec.Limits
	if limits == (execution.Limits{}) {
		limits = execution.DefaultTaskLimits
	}
	out := s.Checks.Run(ctx, execution.Docker, execution.Request{Limits: limits,
		Workspace: workspace, WorkspaceRO: !spec.Writable, ExportImage: spec.ExportImage,
		Image: spec.Image, Argv: spec.Argv, Env: spec.Env, NoEntrypoint: spec.NoEntrypoint,
		Network: network, Timeout: timeout, Report: spec.Report, History: history,
		Files: spec.Files, FilesDir: filepath.Join(dir, "files"), Services: spec.Services,
		Mounts: s.toolCacheMounts(),
		Name:   "apptown-check-" + id,
		Labels: []string{dockerlabel.Class + "=check", s.CheckOwnerLabel(), dockerlabel.Deadline + "=" + strconv.FormatInt(deadline.Unix(), 10)},
	})
	answer := CheckAnswer{Termination: out.Termination, ExitCode: out.ExitCode, Output: tail(out.Output, maxCheckOutput), Report: out.Report}
	if out.RunErr != nil && out.Termination != execution.Exited {
		answer.RunErr = out.RunErr.Error()
	}
	if out.CleanupErr != nil {
		answer.RunErr = strings.TrimSpace(answer.RunErr + "; " + out.CleanupErr.Error())
	}
	if out.ReportErr != nil {
		answer.ReportErr = out.ReportErr.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

// receiveHistory makes a bare repository from the history bundle part: the
// forge's own commits, fetched with the repository's hooks and
// configuration out of reach of the check, which sees it read-only.
func receiveHistory(ctx context.Context, mr *multipart.Reader, dir string) (string, error) {
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "history" {
		return "", errors.New("the history bundle follows the tree")
	}
	bundle := filepath.Join(dir, "history.bundle")
	f, err := os.Create(bundle)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(part, maxCheckHistory+1))
	if err = errors.Join(err, f.Close()); err != nil {
		return "", err
	}
	if n > maxCheckHistory {
		return "", errors.New("larger than the worker accepts")
	}
	repo := filepath.Join(dir, "history")
	if out, err := gitexec.RunEnv(ctx, "", nil, "init", "-q", "--bare", repo); err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	if out, err := gitexec.RunEnv(ctx, repo, nil, "-c", "core.hooksPath=/dev/null", "fetch", "-q", bundle, "+refs/*:refs/*"); err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	return repo, os.Remove(bundle)
}

func checkID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// tail keeps the last n bytes: a check's verdict is at its end.
func tail(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}

// CheckOwnerLabel names this worker on the check containers it starts: its
// installation and its state folder, which no other worker shares. The
// sweep removes only containers so labelled.
func (s *Server) CheckOwnerLabel() string {
	state, err := filepath.Abs(s.State)
	if err != nil {
		state = s.State
	}
	sum := sha256.Sum256([]byte(s.Installation + "\x00" + state))
	return dockerlabel.Worker + "=" + hex.EncodeToString(sum[:8])
}

// SweepChecks removes this worker's check containers and folders past their
// deadline, or
// every one when all is set (the worker's start: a check the worker was
// running when it stopped is discarded, never answered). A removal that
// fails is logged and tried again by the next sweep.
func (s *Server) SweepChecks(ctx context.Context, dk docker.Client, all bool) {
	now := s.now().Unix()
	// Only this worker's checks: another worker on the same Docker runs its
	// own, which are not this one's to end.
	out, err := stdout(ctx, dk, "ps", "-a", "--filter", "label="+dockerlabel.Class+"=check",
		"--filter", "label="+s.CheckOwnerLabel(),
		"--format", `{{.ID}} {{.Label "`+dockerlabel.Deadline+`"}}`)
	if err != nil {
		log.Printf("check sweep: listing check containers: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id, deadline, _ := strings.Cut(line, " ")
		if id == "" {
			continue
		}
		if at, err := strconv.ParseInt(deadline, 10, 64); all || err != nil || at < now {
			if out, err := dk.Run(ctx, "", docker.Command{Args: []string{"rm", "-f", "-v", id}}); err != nil {
				log.Printf("check sweep: removing container %s: %v: %s", id, err, out)
			}
		}
	}
	// A check's services' network, after its containers: one in use is
	// not removed, and the next sweep tries it again.
	out, err = stdout(ctx, dk, "network", "ls", "--filter", "label="+dockerlabel.Class+"=check",
		"--filter", "label="+s.CheckOwnerLabel(),
		"--format", `{{.ID}} {{.Label "`+dockerlabel.Deadline+`"}}`)
	if err != nil {
		log.Printf("check sweep: listing check networks: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id, deadline, _ := strings.Cut(line, " ")
		if id == "" {
			continue
		}
		if at, err := strconv.ParseInt(deadline, 10, 64); all || err != nil || at < now {
			if out, err := dk.Run(ctx, "", docker.Command{Args: []string{"network", "rm", id}}); err != nil {
				log.Printf("check sweep: removing network %s: %v: %s", id, err, out)
			}
		}
	}
	s.sweepCheckImages(ctx, dk)
	entries, err := os.ReadDir(s.checksRoot())
	if err != nil && !os.IsNotExist(err) {
		log.Printf("check sweep: reading %s: %v", s.checksRoot(), err)
	}
	for _, e := range entries {
		deadline, _, _ := strings.Cut(e.Name(), "-")
		if at, err := strconv.ParseInt(deadline, 10, 64); all || err != nil || at < now {
			if err := os.RemoveAll(filepath.Join(s.checksRoot(), e.Name())); err != nil {
				log.Printf("check sweep: removing %s: %v", e.Name(), err)
			}
		}
	}
}

// Images prepares images for checks on the worker's Docker. Nil for a worker
// that runs none.
type imagePreparer interface {
	PrepareImage(ctx context.Context, req execution.ImageRequest) execution.ImageAnswer
}

// checkImage is POST /v1/check-images: a multipart body of the request and,
// for a build, the staged tree its context is under.
func (s *Server) checkImage(w http.ResponseWriter, r *http.Request) {
	images, ok := s.Checks.(imagePreparer)
	if s.Checks == nil || !ok {
		http.Error(w, "this worker prepares no check images", http.StatusNotImplemented)
		return
	}
	ctx, lifetime := bounded(w, r, checkGrace+30*time.Minute)
	defer lifetime.Stop()
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "an image request is multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "spec" {
		http.Error(w, "an image request starts with its spec", http.StatusBadRequest)
		return
	}
	var req execution.ImageRequest
	if err := json.NewDecoder(io.LimitReader(part, maxCheckSpec)).Decode(&req); err != nil {
		http.Error(w, "the image request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Tree = ""
	// Every image request has a folder of its own, named by its deadline:
	// a build's tree, and a login's Docker configuration, which the sweep
	// removes if the request is interrupted.
	id, err := checkID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deadline := s.now().Add(30*time.Minute + checkGrace)
	dir := filepath.Join(s.checksRoot(), fmt.Sprintf("%d-%s", deadline.Unix(), id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("image request %s: removing its folder: %v (the sweep retries)", id, err)
		}
	}()
	req.Scratch = dir
	if req.Context != "" {
		if !localName(req.Context) && req.Context != "." {
			http.Error(w, "the build context must be a folder inside the tree", http.StatusBadRequest)
			return
		}
		req.Tree = filepath.Join(dir, "tree")
		if err := os.MkdirAll(req.Tree, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if part, err = mr.NextPart(); err != nil || part.FormName() != "tree" {
			http.Error(w, "a build's tree follows its spec", http.StatusBadRequest)
			return
		}
		root, err := os.OpenRoot(req.Tree)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = extractBounded(part, root, true, maxCheckTree, maxCheckEntries)
		root.Close()
		if err != nil {
			http.Error(w, "the build's tree: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}
	if err := drained(mr, r.Body); err != nil {
		http.Error(w, "the image request: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.images.RLock()
	answer := images.PrepareImage(ctx, req)
	s.usedImage(answer.ID)
	s.images.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

// maxTrailer bounds what may follow a request's last part: the multipart
// epilogue, never data.
const maxTrailer = 64 << 10

// drained reads the request to its end: no part beyond those read, and at
// most maxTrailer bytes after them. Only a request read to its end lets the
// server notice AppTown going away.
func drained(mr *multipart.Reader, body io.Reader) error {
	if _, err := mr.NextPart(); err != io.EOF {
		if err == nil {
			return errors.New("it has a part beyond those it may have")
		}
		return err
	}
	n, err := io.Copy(io.Discard, io.LimitReader(body, maxTrailer+1))
	if err != nil {
		return err
	}
	if n > maxTrailer {
		return errors.New("it goes on past its last part")
	}
	return nil
}

// lifetime ends a request at its deadline: its context is cancelled, its
// connection's reads stop, and so does any read of its body.
type lifetime struct {
	timer  *time.Timer
	cancel context.CancelFunc
	rc     *http.ResponseController
}

// bounded gives a request a lifetime, first of initial, and a body whose
// reads end with it (a stalled upload, or an AppTown gone away).
func bounded(w http.ResponseWriter, r *http.Request, initial time.Duration) (context.Context, *lifetime) {
	ctx, cancel := context.WithCancel(r.Context())
	l := &lifetime{timer: time.AfterFunc(initial, cancel), cancel: cancel, rc: http.NewResponseController(w)}
	l.readFor(initial)
	r.Body = contextBody{ctx: ctx, ReadCloser: r.Body}
	return ctx, l
}

// For moves the end to d from now, once the request says how long it may
// take.
func (l *lifetime) For(d time.Duration) {
	l.timer.Reset(d)
	l.readFor(d)
}

// readFor ends the connection's reads d from now. A connection's deadline
// is judged on the system clock, so it is set from that clock.
func (l *lifetime) readFor(d time.Duration) {
	_ = l.rc.SetReadDeadline(clock.System().Add(d)) // a test's recorder has no connection
}

func (l *lifetime) Stop() { l.timer.Stop(); l.cancel() }

// contextBody is a request body whose reads end when its context does,
// whatever the connection is doing.
type contextBody struct {
	ctx context.Context
	io.ReadCloser
}

func (b contextBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	type read struct {
		n   int
		err error
	}
	done := make(chan read, 1)
	buf := make([]byte, len(p))
	go func() { n, err := b.ReadCloser.Read(buf); done <- read{n, err} }()
	select {
	case got := <-done:
		copy(p, buf[:got.n])
		return got.n, got.err
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}

// checkImageUse is how long a check image stays after its last use.
const checkImageUse = 24 * time.Hour

func (s *Server) imageUses() string { return filepath.Join(s.State, "check-images") }

// usedImage records that a check image (by ID) was just prepared or used.
func (s *Server) usedImage(id string) {
	name, ok := strings.CutPrefix(id, "sha256:")
	if !ok || !localName(name) {
		return
	}
	if err := os.MkdirAll(s.imageUses(), 0o700); err != nil {
		log.Printf("check images: %v", err)
		return
	}
	// A use is the file's mtime, which the sweep compares with the system
	// clock, so it is stamped from that clock.
	now := clock.System()
	path := filepath.Join(s.imageUses(), name)
	err := os.Chtimes(path, now, now)
	if os.IsNotExist(err) {
		err = os.WriteFile(path, nil, 0o600)
	}
	if err != nil {
		log.Printf("check images: recording a use of %s: %v", id, err)
	}
}

// stdout runs a docker command and answers its standard output alone; a
// failure carries what it said on standard error.
func stdout(ctx context.Context, dk docker.Client, args ...string) ([]byte, error) {
	var out bytes.Buffer
	if said, err := dk.Run(ctx, "", docker.Command{Args: args, Stdout: &out}); err != nil {
		return out.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(said)))
	}
	return out.Bytes(), nil
}

// sweepCheckImages removes images checks built a day after their last use,
// every name it goes by included. An image seen for the first time is given
// its day from now; one a container still uses, running or not, is left
// alone. It runs alone: no preparation or use is recorded meanwhile.
func (s *Server) sweepCheckImages(ctx context.Context, dk docker.Client) {
	s.images.Lock()
	defer s.images.Unlock()
	out, err := stdout(ctx, dk, "images", "-q", "--no-trunc", "--filter", "label="+execution.CheckLabel)
	if err != nil {
		log.Printf("check sweep: listing check images: %v", err)
		return
	}
	for _, id := range strings.Fields(string(out)) {
		name, ok := strings.CutPrefix(id, "sha256:")
		if !ok || !localName(name) {
			continue
		}
		fi, err := os.Stat(filepath.Join(s.imageUses(), name))
		if os.IsNotExist(err) {
			s.usedImage(id)
			continue
		}
		if err != nil || clock.System().Sub(fi.ModTime()) < checkImageUse { // an mtime: the system clock
			continue
		}
		if users, err := stdout(ctx, dk, "ps", "-aq", "--filter", "ancestor="+id); err != nil || strings.TrimSpace(string(users)) != "" {
			continue // in use, or cannot tell: kept, and asked again next time
		}
		// Every name it goes by (two commits may build the same image), which
		// removes it, or its ID when it has none; without force, so a
		// container that took it up meanwhile keeps it.
		names, _ := stdout(ctx, dk, "image", "inspect", "--format", "{{range .RepoTags}}{{.}} {{end}}", id)
		refs := strings.Fields(string(names))
		if len(refs) == 0 {
			refs = []string{id}
		}
		if out, err := dk.Run(ctx, "", docker.Command{Args: append([]string{"rmi"}, refs...)}); err != nil {
			log.Printf("check sweep: removing image %s: %v: %s", id, err, out)
			continue
		}
		_ = os.Remove(filepath.Join(s.imageUses(), name))
	}
}
