package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/flokkit/apptown/internal/execution"
)

// A task's inputs, its workspace's state and its declared result files
// (design/operations/workers.md, "Agent and review steps on the worker"):
//
//   - Inputs are blobs AppTown sent (PUT /v1/blobs/{sum}), copied when the
//     task is ensured into a folder the task owns, outside every workspace,
//     and mounted read-only at their paths under /apptown/in. A blob removed
//     later changes nothing the task sees.
//   - A workspace's state folder is mounted writable at /apptown/state for
//     every task in the workspace, and goes with the workspace. It is beside
//     the tree, not in it: nothing in it is ever collected or committed.
//   - Declared result files are read once the task's container has exited,
//     confined as a check's report is, and kept in the task's own durable
//     record until it is released, so a restarted worker answers the same.

func (s *Server) statePath(workspace string) string {
	return filepath.Join(s.State, "workspace-state", workspace)
}
func (s *Server) taskInputsPath(key string) string { return filepath.Join(s.State, "task-inputs", key) }
func (s *Server) taskResultsPath(key string) string {
	return filepath.Join(s.State, "task-results", key)
}

// checkInputs refuses inputs the worker cannot present: invalid, over the
// bounds by the sizes of the blobs it holds, or naming a blob it lacks or
// that is not one file. Nothing is created.
func (s *Server) checkInputs(inputs []execution.Input) error {
	if err := execution.ValidInputs(inputs); err != nil {
		return err
	}
	var total int64
	for _, in := range inputs {
		fi, err := os.Lstat(s.blobContent(in.Blob))
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w %s (the input %s)", execution.ErrMissingBlob, in.Blob, in.Path)
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("the blob of the input %s is not one file", in.Path)
		}
		if fi.Size() > execution.MaxInputBytes {
			return fmt.Errorf("the input %s is %d bytes, over the %d limit", in.Path, fi.Size(), execution.MaxInputBytes)
		}
		total += fi.Size()
	}
	if total > execution.MaxInputTotalBytes {
		return fmt.Errorf("the inputs are %d bytes, over the %d limit", total, execution.MaxInputTotalBytes)
	}
	return nil
}

// refuseInputs answers a refusal checkInputs gave.
func refuseInputs(w http.ResponseWriter, err error) {
	execution.WriteRefusal(w, http.StatusBadRequest, fmt.Errorf("the task's inputs: %w", err))
}

// checkInputSet answers whether ensuring a task with these inputs would
// refuse them, by the same check. AppTown asks before it uploads a
// task's source, so a refused request leaves no workspace behind: a
// workspace made for a task that is never recorded has nothing to release
// it. It creates nothing.
func (s *Server) checkInputSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Inputs []execution.Input `json:"inputs"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "an invalid input set", http.StatusBadRequest)
		return
	}
	if err := s.checkInputs(req.Inputs); err != nil {
		refuseInputs(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const inputsManifest = "inputs.json"

// stageInputs copies the inputs' blobs into the task's folder and answers
// the mounts presenting each read-only at its path. A folder already there
// (an ensure repeated) is used only if it was staged for the same inputs;
// files a container may have mounted are never rewritten.
func (s *Server) stageInputs(key string, inputs []execution.Input) ([]string, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	dir := s.taskInputsPath(key)
	want, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(dir); err == nil {
		have, err := os.ReadFile(filepath.Join(dir, inputsManifest))
		if err != nil || string(have) != string(want) {
			return nil, fmt.Errorf("%w: the task's inputs were staged for another request", execution.ErrTaskConflict)
		}
		return inputMounts(dir, inputs), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	// Written whole under another name, then renamed: a folder that exists
	// is complete.
	tmp, err := os.MkdirTemp(parent, key+".*.tmp")
	if err != nil {
		return nil, err
	}
	fail := func(err error) ([]string, error) { os.RemoveAll(tmp); return nil, err }
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fail(err)
	}
	for i, in := range inputs {
		if err := copyFile(s.blobContent(in.Blob), filepath.Join(tmp, strconv.Itoa(i)), in.Mode); err != nil {
			return fail(fmt.Errorf("staging the input %s: %w", in.Path, err))
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, inputsManifest), want, 0o600); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return fail(err)
	}
	return inputMounts(dir, inputs), nil
}

func inputMounts(dir string, inputs []execution.Input) []string {
	var mounts []string
	for i, in := range inputs {
		mounts = append(mounts, filepath.Join(dir, strconv.Itoa(i))+":"+in.Path+":ro")
	}
	return mounts
}

// copyFile copies the regular file src to a new file dst with exactly mode.
func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// The umask never narrows what the container reads, or drops the
	// executable bit.
	return os.Chmod(dst, mode)
}

// declaredResults is what a task declared, kept from its ensure.
type declaredResults struct {
	Workspace string                  `json:"workspace"`
	Results   []execution.Result      `json:"results"`
	Images    []execution.ImageResult `json:"images,omitempty"`
}

const (
	declaredFile = "declared.json"
	capturedFile = "captured.json"
)

func (s *Server) writeDeclared(key string, d declaredResults) error {
	if len(d.Results) == 0 && len(d.Images) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.taskResultsPath(key), declaredFile)); err == nil {
		return nil // the same request: its digest covers its results
	}
	body, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return writeDurable(s.taskResultsPath(key), declaredFile, body)
}

func (s *Server) readDeclared(key string) (declaredResults, bool, error) {
	body, err := os.ReadFile(filepath.Join(s.taskResultsPath(key), declaredFile))
	if errors.Is(err, fs.ErrNotExist) {
		return declaredResults{}, false, nil
	}
	if err != nil {
		return declaredResults{}, false, err
	}
	var d declaredResults
	return d, true, json.Unmarshal(body, &d)
}

// resultRoot is the host folder and relative path of a declared result.
func (s *Server) resultRoot(workspace, p string) (string, string) {
	if rel, ok := strings.CutPrefix(p, execution.StateRoot+"/"); ok {
		return s.statePath(workspace), rel
	}
	return s.workspacePath(workspace), strings.TrimPrefix(p, "/work/")
}

// walkResult looks at each part of rel under root without following any
// link. It answers the last part's information, or the result's state when
// the walk says what it is before that: absent, or refused.
func walkResult(root *os.Root, rel string) (fs.FileInfo, *execution.ResultFile, error) {
	parts := strings.Split(rel, "/")
	for i := range parts {
		fi, err := root.Lstat(filepath.Join(parts[:i+1]...))
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			return nil, &execution.ResultFile{State: execution.ResultAbsent}, nil
		case err != nil && strings.Contains(err.Error(), "escapes"):
			return nil, &execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedEscape}, nil
		case err != nil:
			return nil, nil, err
		case fi.Mode()&fs.ModeSymlink != 0:
			return nil, &execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedSymlink}, nil
		case i < len(parts)-1 && !fi.IsDir():
			return nil, &execution.ResultFile{State: execution.ResultAbsent}, nil
		case i == len(parts)-1:
			return fi, nil, nil
		}
	}
	return nil, nil, errors.New("an empty result path")
}

// readResult reads a declared result file, confined as a check's report
// is: a regular file inside the tree or the state folder, reached through
// no link.
func (s *Server) readResult(workspace string, r execution.Result) (execution.ResultFile, error) {
	base, rel := s.resultRoot(workspace, r.Path)
	root, err := os.OpenRoot(base)
	if err != nil {
		return execution.ResultFile{}, fmt.Errorf("the result %s's folder: %w", r.Name, err)
	}
	defer root.Close()
	fi, state, err := walkResult(root, rel)
	if err != nil || state != nil {
		if state == nil {
			return execution.ResultFile{}, err
		}
		return *state, nil
	}
	if !fi.Mode().IsRegular() {
		return execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedNotRegular}, nil
	}
	if fi.Size() > r.Max {
		return execution.ResultFile{State: execution.ResultTooLarge, Size: fi.Size()}, nil
	}
	f, err := root.Open(rel)
	if err != nil {
		return execution.ResultFile{}, err
	}
	defer f.Close()
	// What was opened is what was looked at: nothing replaced it with a
	// link meanwhile.
	if opened, err := f.Stat(); err != nil || !os.SameFile(fi, opened) {
		return execution.ResultFile{State: execution.ResultRefused, Reason: execution.RefusedSymlink}, nil
	}
	body, err := io.ReadAll(io.LimitReader(f, r.Max+1))
	if err != nil {
		return execution.ResultFile{}, err
	}
	if int64(len(body)) > r.Max {
		return execution.ResultFile{State: execution.ResultTooLarge, Size: int64(len(body))}, nil
	}
	return execution.ResultFile{State: execution.ResultPresent, Content: body, Size: int64(len(body))}, nil
}

// errNotExited: the task's results are captured when it has exited.
var errNotExited = errors.New("the task has not exited; its result files are not captured yet")

// captureResults reads a task's declared result files once its container
// has exited and keeps them durably; later calls answer what was kept.
// declared is false for a task that declared none.
func (s *Server) captureResults(ctx context.Context, key string) (files map[string]execution.ResultFile, declared bool, err error) {
	if files, found, err := s.readCaptured(key); found || err != nil {
		return files, true, err
	}
	d, found, err := s.readDeclared(key)
	if !found || err != nil {
		return nil, false, err
	}
	// Exited, and everything beside it confirmed stopped: nothing writes its
	// files any more (observeTask).
	obs, err := s.observeTask(ctx, execution.TaskRef{Key: key})
	if err != nil {
		return nil, true, err
	}
	if obs.State != execution.TaskExited {
		return nil, true, errNotExited
	}
	abandonAt("quiet", key)
	// The images first: the result files' record marks the whole capture
	// done, so a capture interrupted between them is made again.
	if err := s.captureImages(key, d); err != nil {
		return nil, true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if files, found, err := s.readCaptured(key); found || err != nil {
		return files, true, err
	}
	files = map[string]execution.ResultFile{}
	for _, r := range d.Results {
		f, err := s.readResult(d.Workspace, r)
		if err != nil {
			return nil, true, fmt.Errorf("reading the result %s: %w", r.Name, err)
		}
		files[r.Name] = f
	}
	body, err := json.Marshal(files)
	if err != nil {
		return nil, true, err
	}
	return files, true, writeDurable(s.taskResultsPath(key), capturedFile, body)
}

func (s *Server) readCaptured(key string) (map[string]execution.ResultFile, bool, error) {
	body, err := os.ReadFile(filepath.Join(s.taskResultsPath(key), capturedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var files map[string]execution.ResultFile
	return files, true, json.Unmarshal(body, &files)
}

// captureAfterExit captures a task's results when it is seen to have
// exited. A failure is logged: the result read captures them again.
func (s *Server) captureAfterExit(ctx context.Context, key string) {
	if _, _, err := s.captureResults(ctx, key); err != nil && !errors.Is(err, errNotExited) {
		log.Printf("worker: capturing task %s's result files: %v (read again when asked for)", key, err)
	}
}

// results answers a task's captured result files, capturing them first if
// the task has exited and they were not yet.
func (s *Server) results(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, found, err := s.readRecord(key); err != nil || !found {
		if err != nil {
			httpError(w, err)
			return
		}
		http.Error(w, "no such task", http.StatusNotFound)
		return
	}
	files, declared, err := s.captureResults(r.Context(), key)
	switch {
	case errors.Is(err, errNotExited):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		httpError(w, err)
		return
	case !declared:
		files = map[string]execution.ResultFile{}
	}
	writeJSON(w, files)
}

// freshen removes, before a task first starts, any file at the path of each
// result it declared Fresh, so what is read after it exits was left by it.
// A task that has started, or ran, is left as it is: a repeated start never
// removes what the task wrote. Nothing is removed through a link; a folder
// is left, and is refused when the result is read.
func (s *Server) freshen(ctx context.Context, ref execution.TaskRef, d declaredResults) error {
	obs, err := s.Tasks.Observe(ctx, ref)
	if err != nil {
		return err
	}
	if obs.State != execution.TaskCreated {
		return nil
	}
	for _, r := range d.Results {
		if !r.Fresh {
			continue
		}
		if err := s.removeFresh(d.Workspace, r); err != nil {
			return fmt.Errorf("removing the result %s before the task starts: %w", r.Name, err)
		}
	}
	return nil
}

func (s *Server) removeFresh(workspace string, r execution.Result) error {
	base, rel := s.resultRoot(workspace, r.Path)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	// The folders on the way are walked without following a link: through
	// one, nothing is removed, and the result read after the task exits
	// refuses it.
	if dir := filepath.Dir(rel); dir != "." {
		fi, state, err := walkResult(root, dir)
		if err != nil || state != nil || !fi.IsDir() {
			return err
		}
	}
	fi, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.IsDir():
		return nil
	}
	// A file, or a link itself (never its target).
	return root.Remove(rel)
}

// hasFresh reports whether any declared result is Fresh.
func (d declaredResults) hasFresh() bool {
	return slices.ContainsFunc(d.Results, func(r execution.Result) bool { return r.Fresh })
}

// agentImage answers the worker's agent image: the name its build context
// gives (agent_image.go) and its labels, read from the worker's Docker.
func (s *Server) agentImage(w http.ResponseWriter, r *http.Request) {
	if s.AgentImage == "" || s.ImageLabels == nil {
		execution.WriteRefusal(w, http.StatusNotFound, fmt.Errorf("%w: it built none", execution.ErrNoAgentImage))
		return
	}
	labels, err := s.ImageLabels.Labels(r.Context(), s.AgentImage)
	if errors.Is(err, errNoSuchImage) {
		execution.WriteRefusal(w, http.StatusNotFound, fmt.Errorf("%w: %s is not on its Docker", execution.ErrNoAgentImage, s.AgentImage))
		return
	}
	if err != nil {
		httpError(w, err)
		return
	}
	if labels == nil {
		labels = map[string]string{}
	}
	writeJSON(w, execution.AgentImage{Name: s.AgentImage, Labels: labels})
}

var errNoSuchImage = errors.New("no such image")

// Labels answers an image's labels.
func (d DockerImages) Labels(ctx context.Context, image string) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, d.Binary, "image", "inspect", "--format", "{{json (index .Config \"Labels\")}}", image).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no such image") {
			return nil, fmt.Errorf("%w: %s", errNoSuchImage, image)
		}
		return nil, fmt.Errorf("docker image inspect: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var labels map[string]string
	if err := json.Unmarshal(out, &labels); err != nil {
		return nil, fmt.Errorf("reading %s's labels: %w", image, err)
	}
	return labels, nil
}
