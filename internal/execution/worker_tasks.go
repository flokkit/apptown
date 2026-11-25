package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/gitexec"
)

// WorkerTasks is the TaskDriver for a apptown-worker on another machine
// (design/operations/workers.md, "How work travels"). The
// worker shares no files with AppTown: a task's source goes to it as a Git
// bundle of one commit, and its work comes back as a bundle of the commits it
// made (Collect). Host paths, mounts and host routing are refused.
//
// The client must present the installation's certificate; the worker accepts
// no other.
type WorkerTasks struct {
	URL    string
	Client *http.Client
	// Now is the clock a task's admission expiry is judged on before it is
	// sent: the one that wrote it. Nil is the real clock.
	Now func() time.Time
	// MaxResultBytes bounds a returned bundle; zero means 256 MiB.
	MaxResultBytes int64
	// InHand holds the keys this AppTown process has admitted and not yet
	// asked to release, shared by every copy of the client. Nil:
	// the client cannot tell its process's work from a leftover, and
	// Leftovers refuses.
	InHand *KeysInHand
}

// KeysInHand is the work one AppTown process has in hand on its worker: a
// model-judged check or a database dump lives only there, never in
// AppTown's records, between its admission and its release.
type KeysInHand struct {
	mu   sync.Mutex
	keys map[string]bool
}

// add puts a key in hand when it is admitted: the worker makes nothing
// under a key without its grant, except a task in a workspace already held,
// which AppTown records before it ensures it.
func (k *KeysInHand) add(key string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys = map[string]bool{}
	}
	k.keys[key] = true
}

// drop forgets a key once its release is asked, whatever the answer: the
// caller is done with it, and a release that failed is the reconcile's to
// repeat. Recorded work stays protected by its record.
func (k *KeysInHand) drop(key string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, key)
}

func (k *KeysInHand) has(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keys[key]
}

// WorkerHoldings is GET /v1/tasks: every task record and workspace folder
// a worker holds, each under its key.
type WorkerHoldings struct {
	Tasks      []string `json:"tasks"`
	Workspaces []string `json:"workspaces"`
}

// Leftovers lists what the worker holds that this process does not have in
// hand. The worker is listed before the keys in hand are read: anything
// listed was admitted, and so put in hand, before that read; a key released
// in between is released again, which changes nothing. The caller drops
// what AppTown's records still name, read after this.
func (w WorkerTasks) Leftovers(ctx context.Context) (WorkerHoldings, error) {
	if w.InHand == nil {
		return WorkerHoldings{}, errors.New("this worker client does not track the work it has in hand, so it cannot tell a leftover")
	}
	var held WorkerHoldings
	if err := w.call(ctx, http.MethodGet, "/v1/tasks", nil, &held); err != nil {
		return WorkerHoldings{}, err
	}
	out := WorkerHoldings{}
	for _, key := range held.Tasks {
		if !w.InHand.has(key) {
			out.Tasks = append(out.Tasks, key)
		}
	}
	for _, key := range held.Workspaces {
		if !w.InHand.has(key) {
			out.Workspaces = append(out.Workspaces, key)
		}
	}
	return out, nil
}

var _ TaskDriver = WorkerTasks{}

// workerBranch is the branch a task's source is checked out on, on the worker.
const workerBranch = "work"

// A worker presents inputs, keeps result files (inputs.go), captures
// images (image_results.go) and runs private Docker (private.go).
func (w WorkerTasks) Capabilities() Capabilities {
	return Capabilities{Inputs: true, Results: true, Images: true, PrivateDocker: true}
}

// WorkerTaskRequest is the wire form of an ensured task: the contract's
// spec without host paths. AppTown sends it and the worker decodes it, so
// the two cannot disagree on a field. Workspace names a workspace uploaded
// to the worker.
type WorkerTaskRequest struct {
	Key       string    `json:"key"`
	Admission Admission `json:"admission"`
	Image     string    `json:"image"`
	Argv      []string  `json:"argv"`
	Env       []string  `json:"env"`
	Workspace string    `json:"workspace,omitempty"`
	// Source is the commit the workspace was uploaded with.
	Source string `json:"source,omitempty"`
	// Request is the client's identity for what the task runs. The worker
	// holds a key to it; without one, to the request as sent.
	Request     string   `json:"request,omitempty"`
	SourceAtSrc bool     `json:"sourceAtSrc,omitempty"`
	ReadOnly    bool     `json:"readOnly,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	// Files are generated files the task sees read-only, kept in the
	// task's own folder until it is released.
	Files []File `json:"files,omitempty"`
	// Inputs name blobs sent to the worker, presented read-only under
	// /apptown/in; Results are files captured when the task exits.
	Inputs  []Input  `json:"inputs,omitempty"`
	Results []Result `json:"results,omitempty"`
	// Images are OCI image layouts a release build leaves in its
	// workspace's state, captured when it exits.
	Images []ImageResult `json:"images,omitempty"`
	// Private is the task's own network, services and daemon.
	Private *PrivateDocker `json:"private,omitempty"`
}

// ErrWorkspaceGone: the worker answered that the workspace does not exist.
// What was in it is lost; it is never "no changes".
var ErrWorkspaceGone = errors.New("the worker has no such workspace")

// ErrWorkerUnavailable: the worker could not be asked, or its answer could
// not be read. What it did is unknown; it is never absence or refusal.
var ErrWorkerUnavailable = errors.New("the worker could not be reached")

func (w WorkerTasks) client() WorkerClient { return WorkerClient{URL: w.URL, Client: w.Client} }

func (w WorkerTasks) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	var header http.Header
	if contentType != "" {
		header = http.Header{"Content-Type": {contentType}}
	}
	return w.client().Do(ctx, method, path, body, header)
}

// call sends a request and decodes a JSON answer into out (when not nil).
// A refusal (4xx) is the worker's answer; a 5xx or a broken reply is not.
func (w WorkerTasks) call(ctx context.Context, method, path string, in, out any) error {
	return w.client().Call(ctx, method, path, in, out, 32<<20, WorkerRefusal)
}

func taskPath(ref TaskRef, suffix string) string {
	p := "/v1/tasks/" + url.PathEscape(ref.Key) + suffix
	if ref.ProviderID != "" {
		p += "?id=" + url.QueryEscape(ref.ProviderID)
	}
	return p
}

// Ensure sends the task's inputs and has the worker check them, then sends
// its source, if it has one, then asks the worker to create the task. Each
// is idempotent under the task's key.
func (w WorkerTasks) Ensure(ctx context.Context, spec TaskSpec) (TaskObservation, error) {
	if err := spec.Validate(clock.Or(w.Now)()); err != nil {
		return TaskObservation{}, err
	}
	if spec.ReadOnlySource != "" || spec.Workspace != "" || len(spec.Mounts) > 0 || len(spec.ExtraHosts) > 0 || spec.Network != "" {
		return TaskObservation{}, fmt.Errorf("%w: a worker shares no files or host routes with AppTown", ErrUnsupported)
	}
	if err := ValidFiles(spec.Files); err != nil {
		return TaskObservation{}, err
	}
	// The generated files travel with the request: the worker keeps them
	// in the task's own folder. Inputs travel ahead of it, as blobs.
	req := WorkerTaskRequest{Key: spec.Key, Admission: spec.Admission, Image: spec.Image, Argv: spec.Argv, Env: spec.Env, ReadOnly: spec.WorkspaceRO, SourceAtSrc: spec.SourceAtSrc, Labels: spec.Labels, Request: spec.Digest(), Files: spec.Files,
		Inputs: spec.Inputs, Results: spec.Results, Images: spec.Images, Private: spec.Private}
	// A task's workspace is its own, under its key, unless it names one it
	// shares: the worker leaves a shared workspace when the task is released.
	workspace := spec.Key
	if spec.SharedWorkspace != "" {
		if spec.Source == nil {
			return TaskObservation{}, fmt.Errorf("%w: a shared workspace is made from a source commit", ErrUnsupported)
		}
		workspace = spec.SharedWorkspace
	}
	// Inputs the worker would refuse are refused before the source is
	// uploaded: a workspace made for a request the worker then refuses has
	// no task record to release it by.
	if err := w.readyInputs(ctx, spec.Inputs); err != nil {
		return TaskObservation{}, err
	}
	if spec.Source != nil {
		if err := w.sendSource(ctx, workspace, *spec.Source, spec.AgentGit); err != nil {
			return TaskObservation{}, err
		}
		req.Workspace, req.Source = workspace, spec.Source.Commit
	}
	var obs TaskObservation
	err := w.call(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(spec.Key), req, &obs)
	if errors.Is(err, ErrMissingBlob) {
		// The worker removes blobs no chat uses when it starts: one sent
		// before a restart is sent again, once.
		if err := w.sendInputs(ctx, spec.Inputs); err != nil {
			return TaskObservation{}, err
		}
		err = w.call(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(spec.Key), req, &obs)
	}
	return obs, err
}

// readyInputs sends the inputs' blobs the worker lacks and asks it to check
// the set as ensuring the task will.
func (w WorkerTasks) readyInputs(ctx context.Context, inputs []Input) error {
	if len(inputs) == 0 {
		return nil
	}
	check := func() error {
		if err := w.sendInputs(ctx, inputs); err != nil {
			return err
		}
		return w.call(ctx, http.MethodPost, "/v1/inputs/check", struct {
			Inputs []Input `json:"inputs"`
		}{inputs}, nil)
	}
	err := check()
	if errors.Is(err, ErrMissingBlob) {
		// A blob the worker removed between its upload and the check (a
		// restart reaps blobs no chat uses) is sent again, once.
		err = check()
	}
	return err
}

// sendInputs sends each input's blob the worker does not have, once.
func (w WorkerTasks) sendInputs(ctx context.Context, inputs []Input) error {
	sent := map[string]bool{}
	for _, in := range inputs {
		if in.tar == nil || sent[in.Blob] {
			continue
		}
		sent[in.Blob] = true
		if err := w.client().EnsureBlob(ctx, in.Blob, in.tar); err != nil {
			return fmt.Errorf("sending the input %s: %w", in.Path, err)
		}
	}
	return nil
}

// Results reads the result files the worker captured when the task exited,
// by name, each in one of its four states, with each present one's bytes
// read whole into Content (ReadResult). It is for results a caller parses
// whole (verdicts, transcripts), whose declared Max it can hold; a large
// result is read with ResultStates and ReadResult to a file. A task that
// has not exited, or whose results the worker could not capture or serve,
// is an error, never absence.
func (w WorkerTasks) Results(ctx context.Context, ref TaskRef) (map[string]ResultFile, error) {
	out, err := w.ResultStates(ctx, ref)
	if err != nil {
		return nil, err
	}
	for name, f := range out {
		if f.State != ResultPresent {
			continue
		}
		var body bytes.Buffer
		if err := w.ReadResult(ctx, ref, name, f.Size, &body); err != nil {
			return nil, err
		}
		f.Content = body.Bytes()
		out[name] = f
	}
	return out, nil
}

// ResultStates reads what the worker captured of each declared result when
// the task exited, by name: its state, and its size or reason, without its
// bytes. A task that has not exited, or whose results the worker could not
// capture, is an error, never absence.
func (w WorkerTasks) ResultStates(ctx context.Context, ref TaskRef) (map[string]ResultFile, error) {
	resp, err := w.do(ctx, http.MethodGet, taskPath(ref, "/results"), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, WorkerRefusal(resp)
	}
	var out map[string]ResultFile
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: reading the task's result files: %w", ErrWorkerUnavailable, err)
	}
	for name, f := range out {
		if err := f.Valid(); err != nil {
			return nil, fmt.Errorf("%w: the task's result %s: %w", ErrWorkerUnavailable, name, err)
		}
	}
	return out, nil
}

// ReadResult copies a captured present result, of the size its state says,
// to out as the worker streams it. A reply without its length, of another
// length, or cut short is ErrWorkerUnavailable: a short read is never a
// shorter result. A result the worker no longer keeps is the worker's
// refusal.
func (w WorkerTasks) ReadResult(ctx context.Context, ref TaskRef, name string, size int64, out io.Writer) error {
	resp, err := w.do(ctx, http.MethodGet, taskPath(ref, "/results/"+url.PathEscape(name)), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return WorkerRefusal(resp)
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength != size {
		return fmt.Errorf("%w: the result %s came back as %d with length %d, not the %d bytes captured", ErrWorkerUnavailable, name, resp.StatusCode, resp.ContentLength, size)
	}
	n, err := io.Copy(out, resp.Body)
	if err == nil && n != size {
		err = fmt.Errorf("%d of %d bytes", n, size)
	}
	if err != nil {
		return fmt.Errorf("%w: reading the result %s: %w", ErrWorkerUnavailable, name, err)
	}
	return nil
}

// ImageResults reads what the worker captured of each declared image when
// the task exited, by name, each in one of its four states. A task that has
// not exited, or whose images the worker could not capture, is an error,
// never absence; a released task's are ErrImageGone.
func (w WorkerTasks) ImageResults(ctx context.Context, ref TaskRef) (map[string]ImageCapture, error) {
	resp, err := w.do(ctx, http.MethodGet, taskPath(ref, "/images"), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, imageRefusal(resp)
	}
	var out map[string]ImageCapture
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: reading the task's images: %w", ErrWorkerUnavailable, err)
	}
	for name, c := range out {
		if err := c.Valid(); err != nil {
			return nil, fmt.Errorf("%w: the task's image %s: %w", ErrWorkerUnavailable, name, err)
		}
	}
	return out, nil
}

// ImageFile opens one file of a captured image (ImageFilePattern) and
// answers its length as the worker states it. The reader fails, as a
// transport error, if fewer bytes arrive: a short read is never a short
// file. A released task's files are ErrImageGone. The caller checks every
// byte against its descriptor's digest; the worker's word is not trusted.
func (w WorkerTasks) ImageFile(ctx context.Context, ref TaskRef, image, file string) (io.ReadCloser, int64, error) {
	if !ImageFilePattern.MatchString(file) {
		return nil, 0, fmt.Errorf("%q is not a file of an image's layout the worker serves", file)
	}
	resp, err := w.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(ref.Key)+"/images/"+url.PathEscape(image)+"/"+file, nil, "")
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, 0, imageRefusal(resp)
	}
	// Without a stated length a connection closed early would read as the
	// end of the file.
	if resp.StatusCode != http.StatusOK || resp.ContentLength < 0 {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("%w: the image file %s came back as %d without its length", ErrWorkerUnavailable, file, resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

// imageRefusal names a worker's refusal to serve a task's images. Gone is
// ErrImageGone. A conflict is a task whose images are not captured (it has
// not exited, or they were not read yet), never another task under the key.
func imageRefusal(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusGone:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%w: %s", ErrImageGone, strings.TrimSpace(string(msg)))
	case http.StatusConflict:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("the worker has not captured the task's images: %s", strings.TrimSpace(string(msg)))
	}
	return WorkerRefusal(resp)
}

// AgentImage asks the worker for its agent image: the name its build
// context gives and the image's labels. A worker without one answers
// ErrNoAgentImage.
func (w WorkerTasks) AgentImage(ctx context.Context) (AgentImage, error) {
	var out AgentImage
	err := w.call(ctx, http.MethodGet, "/v1/agent-image", nil, &out)
	if err != nil {
		return AgentImage{}, err
	}
	if out.Name == "" {
		return AgentImage{}, fmt.Errorf("%w: the worker named no agent image", ErrWorkerUnavailable)
	}
	return out, nil
}

// Profile is the profile the worker measured when it started
// (WorkerProfile): what its private Docker rests on.
func (w WorkerTasks) Profile(ctx context.Context) (WorkerProfile, error) {
	var out WorkerProfile
	if err := w.call(ctx, http.MethodGet, "/v1/profile", nil, &out); err != nil {
		return WorkerProfile{}, err
	}
	if out.Name == "" {
		return WorkerProfile{}, fmt.Errorf("%w: the worker reported no profile", ErrWorkerUnavailable)
	}
	return out, nil
}

// Disks is each filesystem the worker writes to, as it reads them now
// (GET /v1/disks): its state folder, and its tasks' Docker data.
func (w WorkerTasks) Disks(ctx context.Context) ([]diskspace.Reading, error) {
	var out []diskspace.Reading
	if err := w.call(ctx, http.MethodGet, "/v1/disks", nil, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the worker reported no disk", ErrWorkerUnavailable)
	}
	return out, nil
}

// sendSource uploads the workspace's source. With agentGit the worker also
// makes the task's own repository at /work/.git (AgentGit).
func (w WorkerTasks) sendSource(ctx context.Context, key string, src TaskSource, agentGit *AgentGit) error {
	file, err := os.CreateTemp("", "apptown-source-*.bundle")
	if err != nil {
		return err
	}
	file.Close()
	defer os.Remove(file.Name())
	if err := gitexec.BundleCommit(ctx, src.Repository, src.Commit, workerBranch, file.Name()); err != nil {
		return err
	}
	bundle, err := os.Open(file.Name())
	if err != nil {
		return err
	}
	defer bundle.Close()
	q := url.Values{"ref": {"refs/heads/" + workerBranch}, "commit": {src.Commit}}
	if agentGit != nil {
		q.Set("agentBranch", agentGit.Branch)
		if agentGit.Base != "" {
			q.Set("agentBase", agentGit.Base)
			q.Set("agentBaseCommit", agentGit.BaseCommit)
		}
	}
	resp, err := w.do(ctx, http.MethodPut, "/v1/workspaces/"+url.PathEscape(key)+"?"+q.Encode(), bundle, "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return WorkerRefusal(resp)
	}
	return nil
}

func (w WorkerTasks) Start(ctx context.Context, ref TaskRef) error {
	return w.call(ctx, http.MethodPost, taskPath(ref, "/start"), nil, nil)
}

// Observe reports a task. An unreachable worker is ErrTaskUnobservable,
// never absence.
func (w WorkerTasks) Observe(ctx context.Context, ref TaskRef) (TaskObservation, error) {
	var obs TaskObservation
	if err := w.call(ctx, http.MethodGet, taskPath(ref, ""), nil, &obs); err != nil {
		return TaskObservation{}, fmt.Errorf("%w: %w", ErrTaskUnobservable, err)
	}
	return obs, nil
}

// Wait asks the worker again while it answers that the task is still
// running: each request is bounded on the worker's side.
func (w WorkerTasks) Wait(ctx context.Context, ref TaskRef) (int, error) {
	for {
		var out struct {
			Running  bool `json:"running"`
			ExitCode int  `json:"exitCode"`
		}
		if err := w.call(ctx, http.MethodPost, taskPath(ref, "/wait"), nil, &out); err != nil {
			return -1, err
		}
		if !out.Running {
			return out.ExitCode, nil
		}
	}
}

func (w WorkerTasks) Output(ctx context.Context, ref TaskRef, read OutputRead) (OutputPage, error) {
	path := taskPath(ref, "/output")
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	path += sep + "cursor=" + strconv.FormatInt(read.Cursor, 10) + "&max=" + strconv.Itoa(read.Max)
	var page OutputPage
	return page, w.call(ctx, http.MethodGet, path, nil, &page)
}

func (w WorkerTasks) Stop(ctx context.Context, ref TaskRef) error {
	return w.call(ctx, http.MethodPost, taskPath(ref, "/stop"), nil, nil)
}

// Release removes the task, its own workspace on the worker (not one it
// shares) and the worker's record of it.
func (w WorkerTasks) Release(ctx context.Context, ref TaskRef) error {
	defer w.InHand.drop(ref.Key)
	return w.call(ctx, http.MethodDelete, taskPath(ref, ""), nil, nil)
}

// ReleaseWorkspace removes a shared workspace from the worker. The worker
// refuses while a task naming it is not released (ErrWorkspaceInUse).
func (w WorkerTasks) ReleaseWorkspace(ctx context.Context, key string) error {
	defer w.InHand.drop(key)
	resp, err := w.do(ctx, http.MethodDelete, "/v1/workspaces/"+url.PathEscape(key), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusConflict:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%w: %s", ErrWorkspaceInUse, strings.TrimSpace(string(msg)))
	case resp.StatusCode >= 300:
		return WorkerRefusal(resp)
	}
	return nil
}

// Collect asks the worker for the commits the task made after base, with
// anything it left uncommitted committed under message, and writes them as a
// bundle to dest. It returns the worker's tip, or "" when the task changed
// nothing. The bundle is untrusted: import it with gitexec.Quarantine.
func (w WorkerTasks) Collect(ctx context.Context, key, base, message, dest string) (string, error) {
	q := url.Values{"base": {base}, "message": {message}}
	resp, err := w.do(ctx, http.MethodGet, "/v1/workspaces/"+url.PathEscape(key)+"/result?"+q.Encode(), nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return "", nil
	}
	if resp.StatusCode == http.StatusNotFound {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("%w: %s", ErrWorkspaceGone, strings.TrimSpace(string(msg)))
	}
	if resp.StatusCode >= 300 {
		return "", WorkerRefusal(resp)
	}
	// Only 204 says "no changes". A bundle must name its tip; a reply that
	// does not is malformed, and is asked for again, never taken as empty.
	head := resp.Header.Get("AppTown-Head")
	if resp.StatusCode != http.StatusOK || !isCommitID(head) {
		return "", fmt.Errorf("%w: the task's result came back as %d naming commit %q", ErrWorkerUnavailable, resp.StatusCode, head)
	}
	limit := w.MaxResultBytes
	if limit <= 0 {
		limit = 256 << 20
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(file, io.LimitReader(resp.Body, limit+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("%w: receiving the task's result: %w", ErrWorkerUnavailable, err)
	}
	if n > limit {
		return "", fmt.Errorf("%w: the task's result is larger than %d bytes", gitexec.ErrBundleRefused, limit)
	}
	return head, nil
}

// isCommitID reports whether s is a full SHA-1 or SHA-256 object name.
func isCommitID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// AdmissionAnswer reports the worker's current admission for a key.
type AdmissionAnswer struct {
	Ahead    int  `json:"ahead"`
	Admitted bool `json:"admitted"`
}

func (w WorkerTasks) Admit(ctx context.Context, key string) (int, bool, error) {
	// In hand before the worker can hold anything under it.
	w.InHand.add(key)
	var answer struct {
		Ahead    *int  `json:"ahead"`
		Admitted *bool `json:"admitted"`
	}
	err := w.call(ctx, http.MethodPost, "/v1/admissions/"+url.PathEscape(key), nil, &answer)
	if err != nil {
		return 0, false, err
	}
	if answer.Ahead == nil || answer.Admitted == nil || *answer.Ahead < 0 {
		return 0, false, fmt.Errorf("%w: the admission answer lacks its state or number ahead", ErrWorkerUnavailable)
	}
	return *answer.Ahead, *answer.Admitted, nil
}
