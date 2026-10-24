package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Previews on the worker (design/operations/workers.md, "Previews on the worker"):
// AppTown prepares every input of a boot, the worker boots it with the
// same code on its own Docker and keeps the tree it serves, and AppTown
// forwards visitors to it through the worker's authenticated connection.

const (
	maxPreviewPrepared = 16 << 20
	previewLifetime    = docker.BootLifetime // a boot's upload, build and probe
	// PreviewHostHeader carries the visitor's Host across the worker hop;
	// only AppTown sets it, over the worker's authenticated connection.
	PreviewHostHeader = "X-AppTown-Preview-Host"
	// PreviewTargetHeader carries the visitor's request target (path and
	// query, escaped as sent), so this server's router never cleans or
	// redirects a visitor's path.
	PreviewTargetHeader = "X-AppTown-Preview-Target"
	// PreviewSessionHeader and PreviewSeqHeader are AppTown session and
	// the number within it that a request changing a preview carries (see
	// previewSessions).
	PreviewSessionHeader = "X-AppTown-Preview-Session"
	PreviewSeqHeader     = "X-AppTown-Preview-Seq"
	// PreviewClaimHeader, "required", answers a request refused because the
	// worker's fence is lost: AppTown claims the worker again.
	PreviewClaimHeader = "X-AppTown-Preview-Claim"
)

var (
	// A preview's name under either prefix a host may carry: previews are
	// named apptown-, and one an older release left is still removed by name.
	previewNamePattern  = regexp.MustCompile(`^(apptown|forge)-preview-[a-z0-9][a-z0-9-]{0,100}$`)
	previewStackPattern = regexp.MustCompile(`^pv[a-z0-9][a-z0-9-]{0,100}$`)
)

// PreviewSpec is what a preview boot is, besides its prepared value and tree.
type PreviewSpec struct {
	Name         string `json:"name"`
	StackID      string `json:"stackId"`
	Slug         string `json:"slug"`
	Incarnation  string `json:"incarnation"`
	ProbeSeconds int    `json:"probeSeconds"`
	PageCheck    bool   `json:"pageCheck,omitempty"`
	// VisitorHost is the host a visitor's request carries (BootRequest).
	VisitorHost string `json:"visitorHost,omitempty"`
	// Images, for a release's preview, are the release's images by digest
	// (<repository>@sha256:<hex>): the boot pulls them with the prepared
	// registry login and builds nothing. nil builds the tree, as a change's
	// preview does.
	Images *PreviewImages `json:"images,omitempty"`
}

// PreviewImages are a release's images by part and by init step.
type PreviewImages struct {
	Parts map[string]string `json:"parts"`
	Init  map[string]string `json:"init,omitempty"`
}

// byDigest says whether every image is named by digest.
func (i *PreviewImages) byDigest() bool {
	for _, images := range []map[string]string{i.Parts, i.Init} {
		for _, ref := range images {
			if !docker.IsDigestReference(ref) {
				return false
			}
		}
	}
	return true
}

// PreviewState is a preview on the worker as it is now.
type PreviewState struct {
	Running     bool   `json:"running"`
	Incarnation string `json:"incarnation,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
	// Image and Digest are what the app runs, for a boot's answer.
	Image  string `json:"image,omitempty"`
	Digest string `json:"digest,omitempty"`
	// BrowserValues are the values the app's page is given, read from the
	// container, for a state's answer: null when they cannot be read, and
	// an empty object when the container states there are none.
	BrowserValues map[string]string `json:"browserValues"`
	// Boot failure, for a boot request: the phase, the error and the log.
	Phase   string `json:"phase,omitempty"`
	Error   string `json:"error,omitempty"`
	LogTail string `json:"logTail,omitempty"`
	// Page is what the boot's front-page check saw, for a boot's answer,
	// passed or refused.
	Page *docker.PageWitness `json:"page,omitempty"`
}

func (s *Server) previewsRoot() string { return filepath.Join(s.State, "previews") }

func (s *Server) previewScope() docker.Scope {
	return docker.Scope{Client: s.Previews}
}

// previewLock serializes boots, restarts and removals of one preview, so a
// boot a lost connection left running cannot overlap the next.
func (s *Server) previewLock(name string) func() {
	s.mu.Lock()
	if s.previewLocks == nil {
		s.previewLocks = map[string]*sync.Mutex{}
	}
	m := s.previewLocks[name]
	if m == nil {
		m = &sync.Mutex{}
		s.previewLocks[name] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// fenceToken orders the requests that change previews: an AppTown session
// number, greater for each apptown start, and a request number within it.
type fenceToken struct {
	Session int64 `json:"session"`
	Seq     int64 `json:"seq"`
}

func (a fenceToken) before(b fenceToken) bool {
	return a.Session < b.Session || a.Session == b.Session && a.Seq < b.Seq
}

// requestToken reads a request's token; one without a positive session
// and number is refused before any effect.
func requestToken(r *http.Request) (fenceToken, error) {
	session, err1 := strconv.ParseInt(r.Header.Get(PreviewSessionHeader), 10, 64)
	seq, err2 := strconv.ParseInt(r.Header.Get(PreviewSeqHeader), 10, 64)
	if err1 != nil || err2 != nil || session <= 0 || seq <= 0 {
		return fenceToken{}, errors.New("a request that changes a preview carries its AppTown session and number")
	}
	return fenceToken{session, seq}, nil
}

// previewFence fences requests that change previews, so that one delayed
// past a later one never undoes it. Each preview keeps the token of the
// last request that changed it, and the latest claim (AppTown's start-up
// removal of every preview) sets a floor under all of them. A request, or a
// claim, older than either is refused. It is kept on the worker's disk
// (fence.json), so a worker that starts again keeps it with the previews
// it keeps.
type previewFence struct {
	loaded bool
	// Lost: the worker keeps previews whose order is not known (its fence
	// file was gone). Nothing may change them until a claim has removed
	// them all (PreviewClaimHeader). It is kept in the file: a claim writes
	// its floor with it still set, and clears it only once every kept
	// preview is gone, so a write that failed half way, or a restart,
	// leaves it set.
	Lost  bool                  `json:"lost,omitempty"`
	Floor fenceToken            `json:"floor"`
	Last  map[string]fenceToken `json:"last"`
}

// errClaimRequired is a request refused because the worker's fence is
// lost: AppTown claims the worker again.
var errClaimRequired = errors.New("this worker lost the order of its previews' requests; a claim must come first")

func (s *Server) fencePath() string { return filepath.Join(s.previewsRoot(), "fence.json") }

// fenceLocked reads the fence once; s.mu is held.
func (s *Server) fenceLocked() error {
	if s.fence.loaded {
		return nil
	}
	body, err := os.ReadFile(s.fencePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		// No fence, and previews kept: their order is lost, not new.
		entries, _ := os.ReadDir(s.previewsRoot())
		for _, e := range entries {
			if previewNamePattern.MatchString(e.Name()) {
				s.fence.Lost = true
			}
		}
	}
	if err == nil {
		if err := json.Unmarshal(body, &s.fence); err != nil {
			return fmt.Errorf("the previews' fence cannot be read: %w", err)
		}
	}
	if s.fence.Last == nil {
		s.fence.Last = map[string]fenceToken{}
	}
	s.fence.loaded = true
	return nil
}

// saveFenceLocked writes the fence before the request it admits has any
// effect; s.mu is held.
func (s *Server) saveFenceLocked() error {
	body, err := json.Marshal(s.fence)
	if err != nil {
		return err
	}
	// Durable before the request it admits has any effect.
	return writeDurable(s.previewsRoot(), "fence.json", body)
}

// admitRequest, under the preview's lock, says whether a request with
// token t may change the preview, and if so records it as the last.
func (s *Server) admitRequest(name string, t fenceToken) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fenceLocked(); err != nil {
		return false, err
	}
	if s.fence.Lost {
		return false, errClaimRequired
	}
	if t.before(s.fence.Floor) || t.before(s.fence.Last[name]) {
		return false, nil
	}
	// Admitted only once on disk; a failed write leaves the fence as it was.
	prev, had := s.fence.Last[name]
	s.fence.Last[name] = t
	if err := s.saveFenceLocked(); err != nil {
		if had {
			s.fence.Last[name] = prev
		} else {
			delete(s.fence.Last, name)
		}
		return false, err
	}
	return true, nil
}

// claimFor admits a claim with token t: it raises the floor, and answers
// the names earlier requests were admitted for, a boot not yet visible in a
// folder or a container among them. A claim older than the floor is
// refused: a later one was made.
func (s *Server) claimFor(t fenceToken) ([]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fenceLocked(); err != nil {
		return nil, false, err
	}
	// Older than the floor is refused, lost or not: a recovery under way has
	// its floor on disk already, and an older claim must not lower it.
	if t.before(s.fence.Floor) {
		return nil, false, nil
	}
	// A claim makes a new order. It holds only once it is on disk: a claim
	// whose write fails changes nothing in memory. After a lost fence the
	// claim keeps Lost set, on disk too, until it has removed every kept
	// preview (clearLost).
	floor := s.fence.Floor
	s.fence.Floor = t
	if err := s.saveFenceLocked(); err != nil {
		s.fence.Floor = floor
		return nil, false, err
	}
	var admitted []string
	for name := range s.fence.Last {
		admitted = append(admitted, name)
	}
	return admitted, true, nil
}

// clearLost ends a lost fence, once a claim has removed every kept preview:
// kept on disk first, and only then in memory.
func (s *Server) clearLost() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fence.Lost {
		return nil
	}
	s.fence.Lost = false
	if err := s.saveFenceLocked(); err != nil {
		s.fence.Lost = true
		return err
	}
	return nil
}

// forgetRemoved, under the preview's lock, forgets a preview a claim with
// token t removed, unless a later request changed it: the floor fences
// every request it held back, so the fence does not grow with every
// preview there ever was.
func (s *Server) forgetRemoved(name string, t fenceToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.before(s.fence.Last[name]) {
		return nil
	}
	if _, ok := s.fence.Last[name]; !ok {
		return nil
	}
	delete(s.fence.Last, name)
	return s.saveFenceLocked()
}

// changedSince, under the preview's lock, says whether a request later than
// t has changed the preview: a claim with token t leaves it.
func (s *Server) changedSince(name string, t fenceToken) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t.before(s.fence.Last[name])
}

// admit answers a request that may not change the preview: one without its
// token, one overtaken by a later request, or one whose admission cannot be
// recorded.
func (s *Server) admit(w http.ResponseWriter, r *http.Request, name string) bool {
	t, err := requestToken(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	ok, err := s.admitRequest(name, t)
	switch {
	case errors.Is(err, errClaimRequired):
		w.Header().Set(PreviewClaimHeader, "required")
		http.Error(w, err.Error(), http.StatusConflict)
		return false
	case err != nil:
		http.Error(w, "recording the request's admission: "+err.Error(), http.StatusInternalServerError)
		return false
	case !ok:
		http.Error(w, "a later request has changed this preview since this one was sent", http.StatusConflict)
		return false
	}
	return true
}

// tryPreviewLock is previewLock for the sweep: a preview being booted or
// removed is left to that request.
func (s *Server) tryPreviewLock(name string) (func(), bool) {
	s.mu.Lock()
	if s.previewLocks == nil {
		s.previewLocks = map[string]*sync.Mutex{}
	}
	m := s.previewLocks[name]
	if m == nil {
		m = &sync.Mutex{}
		s.previewLocks[name] = m
	}
	s.mu.Unlock()
	if !m.TryLock() {
		return nil, false
	}
	return m.Unlock, true
}

func (s *Server) previewName(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.Previews == nil {
		http.Error(w, "this worker runs no previews", http.StatusNotImplemented)
		return "", false
	}
	name := r.PathValue("name")
	if !previewNamePattern.MatchString(name) {
		http.Error(w, "not a preview name", http.StatusBadRequest)
		return "", false
	}
	return name, true
}

// state reads a preview's container: running, which incarnation, its ID.
func (s *Server) previewState(ctx context.Context, name string) (PreviewState, error) {
	out, err := s.previewScope().Command(ctx, docker.Command{Args: []string{"inspect", "-f",
		`{{.State.Running}} {{.Id}} {{index .Config.Labels "` + dockerlabel.Incarnation + `"}}`, name}})
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no such") {
			return PreviewState{}, nil
		}
		return PreviewState{}, fmt.Errorf("asking Docker for the preview: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return PreviewState{}, fmt.Errorf("Docker's answer for the preview is not one: %q", out)
	}
	st := PreviewState{Running: fields[0] == "true", ContainerID: fields[1]}
	if len(fields) > 2 {
		st.Incarnation = fields[2]
	}
	return st, nil
}

// A preview's folder holds its spec (spec.json, written before its boot),
// its tree, its boot's Docker configuration while it boots, and one of two
// marks: booted (its incarnation, written once the boot answered and the
// app was found running) or removing (a removal that failed). Only a
// running preview whose folder says it booted that incarnation, and is not
// being removed, is ever reused; anything else is removed.
const (
	previewBootedMark   = "booted"
	previewRemovingMark = "removing"
)

func (s *Server) previewFile(name, file string) string {
	return filepath.Join(s.previewsRoot(), name, file)
}

// booted is the incarnation a preview's folder says its boot completed, ""
// for none, or a preview being removed.
func (s *Server) booted(name string) string {
	if _, err := os.Stat(s.previewFile(name, previewRemovingMark)); err == nil {
		return ""
	}
	b, err := os.ReadFile(s.previewFile(name, previewBootedMark))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// removePreview removes a preview's whole stack (app, parts, init
// leftovers, services, network, and their disposable volumes) and its
// folder. A change's preview keeps the images its boot built: a reopen
// builds from them, and AppTown removes them thirty days after the last
// look (removePreviewImages). Any other preview's images go with it: a
// run's or a release's is never booted again by name. A removal that
// fails keeps the folder, marked, for the sweep to try again.
func (s *Server) removePreview(ctx context.Context, name, stack string) error {
	dk := s.previewScope()
	err := docker.RemovePreview(ctx, dk, name, stack)
	if _, change := docker.ParsePreviewChangeID(name); err == nil && !change {
		err = removeBuildImages(ctx, dk, name)
	}
	dir := filepath.Join(s.previewsRoot(), name)
	if err != nil {
		if _, statErr := os.Stat(dir); statErr == nil {
			_ = os.WriteFile(filepath.Join(dir, previewRemovingMark), nil, 0o600)
		}
		return err
	}
	return os.RemoveAll(dir)
}

// removeBuildImages removes the build images a preview's boot made: its
// app's and its parts'. An image a container still uses stays.
func removeBuildImages(ctx context.Context, dk docker.Scope, name string) error {
	refs, err := dk.BuildImages(ctx, name)
	if err != nil {
		return err
	}
	var failed []error
	for _, ref := range refs {
		failed = append(failed, dk.RemoveUnusedImage(ctx, ref))
	}
	return errors.Join(failed...)
}

// SweepPreviews removes, from the previews no request holds, each whose
// removal failed and each whose boot never completed (a boot holds its
// preview's lock from start to answer, so one found without it and without
// the booted mark was interrupted). Starting, nothing boots yet: it also
// removes each preview that is not running, and the boot configurations the
// stop left in those it keeps.
func (s *Server) SweepPreviews(ctx context.Context, starting bool) {
	if s.Previews == nil {
		return
	}
	entries, err := os.ReadDir(s.previewsRoot())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("sweeping previews: %v", err)
		}
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !previewNamePattern.MatchString(name) {
			continue
		}
		unlock, ok := s.tryPreviewLock(name)
		if !ok {
			continue
		}
		remove := s.booted(name) == ""
		if !remove && starting {
			st, err := s.previewState(ctx, name)
			remove = err == nil && !st.Running
		}
		if remove {
			if err := s.removePreview(ctx, name, s.recordedStack(name)); err != nil {
				log.Printf("sweeping preview %s: %v", name, err)
			}
		} else if starting {
			if err := docker.SweepBootConfigs(filepath.Join(s.previewsRoot(), name)); err != nil {
				log.Printf("sweeping preview %s: %v", name, err)
			}
		}
		unlock()
	}
}

func (s *Server) recordedStack(name string) string {
	body, err := os.ReadFile(filepath.Join(s.previewsRoot(), name, "spec.json"))
	if err != nil {
		return ""
	}
	var spec PreviewSpec
	if json.Unmarshal(body, &spec) != nil || !previewStackPattern.MatchString(spec.StackID) {
		return ""
	}
	return spec.StackID
}

// bootPreview is PUT /v1/previews/{name}: a multipart body of the spec, the
// prepared value and the tree. The same incarnation still running is kept;
// anything else is removed and booted afresh, into a tree of its own.
func (s *Server) bootPreview(w http.ResponseWriter, r *http.Request) {
	name, ok := s.previewName(w, r)
	if !ok {
		return
	}
	ctx, life := bounded(w, r, previewLifetime)
	defer life.Stop()
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "a preview boot is multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	var spec PreviewSpec
	var prepared docker.Prepared
	for _, part := range []struct {
		name string
		into any
	}{{"spec", &spec}, {"prepared", &prepared}} {
		p, err := mr.NextPart()
		if err != nil || p.FormName() != part.name {
			http.Error(w, "a preview boot sends its "+part.name+" here", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(io.LimitReader(p, maxPreviewPrepared)).Decode(part.into); err != nil {
			http.Error(w, "the preview's "+part.name+": "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if spec.Name != name || !previewStackPattern.MatchString(spec.StackID) || spec.Incarnation == "" || spec.ProbeSeconds <= 0 {
		http.Error(w, "a preview boot needs its name, stack, incarnation and probe time", http.StatusBadRequest)
		return
	}
	if spec.Images != nil && !spec.Images.byDigest() {
		http.Error(w, "a release preview's images are named by digest", http.StatusBadRequest)
		return
	}
	if prepared.Version != docker.PreparedVersion {
		http.Error(w, fmt.Sprintf("a prepared boot of version %d, where this worker reads %d", prepared.Version, docker.PreparedVersion), http.StatusBadRequest)
		return
	}
	if prepared.Contract == nil || prepared.Contract.PublicPart() == nil {
		http.Error(w, "a prepared boot carries the contract it was prepared for", http.StatusBadRequest)
		return
	}
	defer s.previewLock(name)()
	if !s.admit(w, r, name) {
		return
	}

	// The same incarnation, running, whose boot completed, is the preview:
	// nothing to boot.
	current, err := s.previewState(ctx, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if current.Running && current.Incarnation == spec.Incarnation && s.booted(name) == spec.Incarnation {
		if err := drainTree(mr, r.Body); err != nil {
			http.Error(w, "the preview boot's request: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, current)
		return
	}
	// A part or need larger than this worker's daemon can give a container
	// is refused before the earlier incarnation is removed.
	if err := docker.AdmitResources(ctx, s.previewScope(), docker.ResourceAdmissionContract(prepared.Contract, prepared.Services)); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	// The earlier incarnation goes first, and wholly: a tree still mounted
	// by a container that survived is never written again.
	if err := s.removePreview(ctx, name, s.recordedStack(name)); err != nil {
		http.Error(w, "removing the earlier preview: "+err.Error(), http.StatusBadGateway)
		return
	}
	// From here, whatever fails leaves nothing of this preview: its stack
	// and folder go, or stay marked for the sweep.
	fail := func(code int, msg string) {
		if err := s.removePreview(ctx, name, spec.StackID); err != nil {
			log.Printf("preview %s: removing a failed boot: %v", name, err)
		}
		http.Error(w, msg, code)
	}
	dir := filepath.Join(s.previewsRoot(), name)
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	body, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), body, 0o600); err != nil {
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	p, err := mr.NextPart()
	if err != nil || p.FormName() != "tree" {
		fail(http.StatusBadRequest, "a preview boot's tree follows its prepared value")
		return
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		fail(http.StatusInternalServerError, err.Error())
		return
	}
	_, err = extractBounded(p, root, true, maxCheckTree, maxCheckEntries)
	root.Close()
	if err == nil {
		err = drained(mr, r.Body)
	}
	if err != nil {
		fail(http.StatusUnprocessableEntity, "the preview's tree: "+err.Error())
		return
	}
	// The boot has a lifetime of its own: an AppTown that stops waiting does
	// not stop it half way, and every command it runs ends when that
	// lifetime does. The contract booted is the one the values were
	// prepared for, never read again from the tree.
	bootLife, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), previewLifetime)
	defer cancel()
	var recorded *docker.RecordedImages
	if spec.Images != nil {
		recorded = &docker.RecordedImages{Parts: spec.Images.Parts, Init: spec.Images.Init, Login: prepared.Registry, Scratch: dir}
	}
	// The boot's images, from the tool versions current as it begins.
	res, err := docker.NewHost(prepared.Dependencies(&docker.Probe{}, s.toolVersions().Images)).Boot(bootLife, docker.BootRequest{
		Docker: s.previewScope().WithLifetime(bootLife), Name: name, StackID: spec.StackID, Workspace: tree, Slug: spec.Slug,
		Deployment: "preview", Probe: time.Duration(spec.ProbeSeconds) * time.Second, PageCheck: spec.PageCheck, VisitorHost: spec.VisitorHost,
		Services: prepared.Services, Incarnation: spec.Incarnation, Scratch: dir, Recorded: recorded,
	}, prepared.Contract)
	if err != nil {
		be := docker.AsBootError(err)
		if rerr := s.removePreview(ctx, name, spec.StackID); rerr != nil {
			log.Printf("preview %s: removing a failed boot: %v", name, rerr)
		}
		writeJSON(w, PreviewState{Phase: be.Phase, Error: be.Err.Error(), LogTail: be.LogTail, Page: res.Page})
		return
	}
	st, err := s.previewState(bootLife, name)
	if err != nil || !st.Running || st.Incarnation != spec.Incarnation {
		fail(http.StatusBadGateway, fmt.Sprintf("the preview booted, and then could not be found running: %v", err))
		return
	}
	if err := os.WriteFile(filepath.Join(dir, previewBootedMark), []byte(spec.Incarnation+"\n"), 0o600); err != nil {
		fail(http.StatusInternalServerError, "recording the preview's boot: "+err.Error())
		return
	}
	st.Page = res.Page
	if st.Image = res.Image(); st.Image != "" {
		st.Digest = s.previewScope().ImageDigest(ctx, st.Image)
	}
	writeJSON(w, st)
}

// drainTree reads a boot's tree part and the rest of the request, unused: a
// preview kept running needs no new tree. The whole request is read before
// answering, as for any request.
func drainTree(mr *multipart.Reader, body io.Reader) error {
	p, err := mr.NextPart()
	if err != nil || p.FormName() != "tree" {
		return errors.New("a preview boot's tree follows its prepared value")
	}
	if n, err := io.Copy(io.Discard, io.LimitReader(p, maxCheckTree+1)); err != nil {
		return err
	} else if n > maxCheckTree {
		return errors.New("the tree is larger than the worker accepts")
	}
	return drained(mr, body)
}

func (s *Server) getPreview(w http.ResponseWriter, r *http.Request) {
	name, ok := s.previewName(w, r)
	if !ok {
		return
	}
	st, err := s.previewState(r.Context(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// The browser values, from the same container: another one's are not
	// its answer.
	if st.Running {
		if installed, err := docker.InspectBrowserValues(r.Context(), s.previewScope(), name); err == nil && installed.ID == st.ContainerID {
			st.BrowserValues = installed.Values
		}
	}
	writeJSON(w, st)
}

// deletePreview is DELETE /v1/previews/{name}: the whole preview goes. A
// removal that fails says so, and AppTown asks again.
func (s *Server) deletePreview(w http.ResponseWriter, r *http.Request) {
	name, ok := s.previewName(w, r)
	if !ok {
		return
	}
	defer s.previewLock(name)()
	if !s.admit(w, r, name) {
		return
	}
	if err := s.removePreview(r.Context(), name, s.recordedStack(name)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st, err := s.previewState(r.Context(), name); err != nil || st.ContainerID != "" {
		http.Error(w, fmt.Sprintf("the preview is still there: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deletePreviewImages is DELETE /v1/previews/{name}/images: the build
// images a stopped preview kept go, when AppTown says its keep is over.
// One a container still uses stays, and that is not a failure.
func (s *Server) deletePreviewImages(w http.ResponseWriter, r *http.Request) {
	name, ok := s.previewName(w, r)
	if !ok {
		return
	}
	defer s.previewLock(name)()
	if !s.admit(w, r, name) {
		return
	}
	if err := removeBuildImages(r.Context(), s.previewScope(), name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TrimBuildCache keeps this worker's build cache within AppTown's budget,
// as AppTown keeps its own (docker.TrimBuildCache): the previews and
// release builds it runs fill it, and nothing else trims it.
func (s *Server) TrimBuildCache(ctx context.Context) {
	if s.Docker == nil {
		return
	}
	if err := docker.TrimBuildCache(ctx, docker.Scope{Client: s.Docker}); err != nil {
		log.Printf("trimming the build cache: %v", err)
	}
}

// listPreviews is GET /v1/previews: the name of every preview this worker
// has, by its folder or its app container. A Docker that cannot list its
// previews is an error, never an empty list.
func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	if s.Previews == nil {
		writeJSON(w, []string{})
		return
	}
	names, err := s.previewNames(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, names)
}

// previewNames are the previews by their folders and their app containers,
// sorted.
func (s *Server) previewNames(ctx context.Context) ([]string, error) {
	names := map[string]bool{}
	entries, err := os.ReadDir(s.previewsRoot())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		names[e.Name()] = true
	}
	running, err := s.previewScope().List(ctx, "ps", "-a", "--filter", "label="+dockerlabel.Class+"=preview", "--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("listing the previews: %w", err)
	}
	for _, name := range running {
		names[strings.TrimPrefix(strings.TrimSpace(name), "/")] = true
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if previewNamePattern.MatchString(name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// deletePreviews is DELETE /v1/previews: AppTown starting claims the
// worker for its session, and every preview goes (it boots them again when
// asked); requests of earlier sessions are refused from then on. Each
// is found by its folder or its app container, and removed wholly under its
// own lock. A Docker that cannot list its previews is not an answer that
// there are none.
func (s *Server) deletePreviews(w http.ResponseWriter, r *http.Request) {
	if s.Previews == nil {
		w.WriteHeader(http.StatusNoContent) // a worker that runs no previews has none
		return
	}
	t, err := requestToken(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	admitted, ok, err := s.claimFor(t)
	switch {
	case err != nil:
		http.Error(w, "recording the claim: "+err.Error(), http.StatusInternalServerError)
		return
	case !ok:
		http.Error(w, "a later claim has been made on this worker's previews", http.StatusConflict)
		return
	}
	var failed []string
	names, err := s.previewNames(r.Context())
	if err != nil {
		// What the folders name still goes; the failure is reported.
		failed = append(failed, err.Error())
		entries, _ := os.ReadDir(s.previewsRoot())
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}
	all := map[string]bool{}
	for _, name := range append(names, admitted...) {
		if previewNamePattern.MatchString(name) {
			all[name] = true
		}
	}
	// Every preview goes, under its lock: a boot an earlier request admitted
	// holds it, and is removed once it has finished. One a later request has
	// changed is not this claim's to take, however long this claim took.
	for _, name := range slices.Sorted(maps.Keys(all)) {
		unlock := s.previewLock(name)
		if !s.changedSince(name, t) {
			if err := s.removePreview(r.Context(), name, s.recordedStack(name)); err != nil {
				failed = append(failed, name+": "+err.Error())
			} else if err := s.forgetRemoved(name, t); err != nil {
				failed = append(failed, name+": "+err.Error())
			}
		}
		unlock()
	}
	// Every kept preview is gone: a lost order is a new one now.
	if len(failed) == 0 {
		if err := s.clearLost(); err != nil {
			failed = append(failed, "recording the new order: "+err.Error())
		}
	}
	if len(failed) > 0 {
		http.Error(w, strings.Join(failed, "; "), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// forwardPreview is /v1/previews/{name}/http: a visitor's request,
// forwarded by AppTown, to that preview's app on this machine's loopback,
// and nothing else. The app sees the visitor's Host and request target, and
// AppTown's forwarding headers; upgrades pass through.
func (s *Server) forwardPreview(w http.ResponseWriter, r *http.Request) {
	name, ok := s.previewName(w, r)
	if !ok {
		return
	}
	target, err := url.ParseRequestURI(r.Header.Get(PreviewTargetHeader))
	if err != nil || !strings.HasPrefix(target.Path, "/") || target.Host != "" {
		http.Error(w, "a forwarded request names its target", http.StatusBadRequest)
		return
	}
	port, err := docker.ContainerHostPort(r.Context(), s.previewScope(), name, 0)
	if err != nil || port == 0 {
		// A name this worker holds no preview of is not a failing
		// upstream: there is nothing there.
		if _, statErr := os.Stat(filepath.Join(s.previewsRoot(), name)); errors.Is(statErr, os.ErrNotExist) {
			http.Error(w, "no such preview", http.StatusNotFound)
			return
		}
		http.Error(w, "the preview is not running", http.StatusBadGateway)
		return
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(req *httputil.ProxyRequest) {
		req.Out.URL = &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port),
			Path: target.Path, RawPath: target.RawPath, RawQuery: target.RawQuery, ForceQuery: target.ForceQuery}
		req.Out.Host = req.In.Header.Get(PreviewHostHeader)
		req.Out.Header.Del(PreviewHostHeader)
		req.Out.Header.Del(PreviewTargetHeader)
		// AppTown's forwarding headers, which it set for this visitor,
		// cross this hop as they are, and no others: this handler is reached
		// only over AppTown's authenticated connection.
		StripForwarding(req.Out.Header)
		for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
			if v := req.In.Header.Values(h); len(v) > 0 {
				req.Out.Header[h] = v
			}
		}
	}}
	proxy.ServeHTTP(w, r)
}

// admitPreview answers whether this worker's daemon can give each part and
// need of a contract the resources it declares (docker.AdmitResources),
// without doing anything: AppTown asks before it registers a preview's
// keys or removes the one running, so a refusal changes nothing.
func (s *Server) admitPreview(w http.ResponseWriter, r *http.Request) {
	var c appcontract.Contract
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPreviewPrepared)).Decode(&c); err != nil {
		http.Error(w, "a preview admission sends the contract: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx, life := bounded(w, r, checkGrace)
	defer life.Stop()
	if err := docker.AdmitResources(ctx, s.previewScope(), &c); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
