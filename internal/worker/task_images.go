package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/execution"
)

// A release build's images (design/operations/workers.md, "Releases built on
// the worker"): each declared image is an OCI layout the build left in its
// workspace's state. Once the task has exited and its private Docker is
// confirmed stopped, nothing writes there; the worker then copies each
// layout, confined as a result file is, into the task's own folder, and
// records what it found. Only the record makes a capture visible, and it is
// written after every file. The copies are kept until the task is released
// and served file by file: the index and its blobs.
//
// Accepted: a capture uses the worker's disk twice over until the task is
// released (the layout in the state folder, and its copy). A copy that
// would leave less than the floor free (diskspace.AdmitAdding) is refused,
// and the capture records the refusal with what is free; a disk that fills
// during the copy anyway fails the capture, which is asked again.

const imagesFile = "images.json"

func (s *Server) taskImagesPath(key string) string {
	return filepath.Join(s.taskResultsPath(key), "images")
}

// captureImages copies each declared image of a task that has exited and
// keeps the record of what it found. It runs under the task's lock, which
// release takes too, so no capture follows a release; a capture already
// recorded is kept.
func (s *Server) captureImages(key string, d declaredResults) error {
	if len(d.Images) == 0 {
		return nil
	}
	unlock := s.lockTask(key)
	defer unlock()
	if s.releasing(key) {
		return execution.ErrImageGone
	}
	if _, found, err := s.readImages(key); found || err != nil {
		return err
	}
	// What an interrupted capture copied is not recorded: it goes, and is
	// copied again from the layouts, which nothing has changed.
	dir := s.taskImagesPath(key)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	captures := map[string]execution.ImageCapture{}
	for _, img := range d.Images {
		c, err := s.captureImage(d.Workspace, img, filepath.Join(dir, img.Name))
		if err != nil {
			return fmt.Errorf("capturing the image %s: %w", img.Name, err)
		}
		captures[img.Name] = c
	}
	body, err := json.Marshal(captures)
	if err != nil {
		return err
	}
	return writeDurable(s.taskResultsPath(key), imagesFile, body)
}

func (s *Server) readImages(key string) (map[string]execution.ImageCapture, bool, error) {
	body, err := os.ReadFile(filepath.Join(s.taskResultsPath(key), imagesFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var captures map[string]execution.ImageCapture
	return captures, true, json.Unmarshal(body, &captures)
}

// captureImage looks at a declared image's folder without following any
// link and, if it is a folder of regular files within the admitted size,
// copies them to dest. Anything else is its state, and nothing is copied.
func (s *Server) captureImage(workspace string, img execution.ImageResult, dest string) (execution.ImageCapture, error) {
	base, rel := s.resultRoot(workspace, img.Path)
	root, err := os.OpenRoot(base)
	if err != nil {
		return execution.ImageCapture{}, fmt.Errorf("the workspace's state: %w", err)
	}
	defer root.Close()
	fi, state, err := walkResult(root, rel)
	if err != nil {
		return execution.ImageCapture{}, err
	}
	if state != nil {
		return execution.ImageCapture{State: state.State, Reason: state.Reason}, nil
	}
	if !fi.IsDir() {
		return refusedImage(execution.RefusedNotFolder), nil
	}
	// The folder's own entries are read as they are, never through a link.
	type entry struct {
		rel  string
		info fs.FileInfo
	}
	var files []entry
	var dirs []string
	var total int64
	var refused string
	err = fs.WalkDir(root.FS(), rel, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			refused = execution.RefusedSymlink
			return fs.SkipAll
		case e.IsDir():
			dirs = append(dirs, p)
			return nil
		case !e.Type().IsRegular():
			refused = execution.RefusedNotRegular
			return fs.SkipAll
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		files = append(files, entry{p, info})
		return nil
	})
	switch {
	case err != nil:
		return execution.ImageCapture{}, err
	case refused != "":
		return refusedImage(refused), nil
	case total > img.Max:
		return execution.ImageCapture{State: execution.ResultTooLarge, Size: total}, nil
	}
	// The copy doubles the image on this disk: refused, with what is free,
	// when it would leave the state folder short of space. The refusal is
	// the capture's answer, so the release build fails saying why rather
	// than being asked again into a full disk.
	space, err := s.stateDisk()
	if err := diskspace.AdmitAdding("the worker's state folder ("+s.State+")", space, err, total); err != nil {
		return execution.ImageCapture{State: execution.ResultRefused, Reason: err.Error()}, nil
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(relTo(rel, d))), 0o700); err != nil {
			return execution.ImageCapture{}, err
		}
	}
	for _, f := range files {
		if err := copyCaptured(root, f.rel, f.info, filepath.Join(dest, filepath.FromSlash(relTo(rel, f.rel)))); err != nil {
			return execution.ImageCapture{}, err
		}
	}
	return execution.ImageCapture{State: execution.ResultPresent, Size: total}, nil
}

func refusedImage(reason string) execution.ImageCapture {
	return execution.ImageCapture{State: execution.ResultRefused, Reason: reason}
}

// relTo is p, a path under the image's folder, relative to it.
func relTo(folder, p string) string {
	if p == folder {
		return "."
	}
	return p[len(folder)+1:]
}

// copyCaptured copies the regular file at p, as looked at, to a new file
// dst, flushed to disk before the capture is recorded.
func copyCaptured(root *os.Root, p string, looked fs.FileInfo, dst string) error {
	in, err := root.Open(p)
	if err != nil {
		return err
	}
	defer in.Close()
	if opened, err := in.Stat(); err != nil || !os.SameFile(looked, opened) {
		return fmt.Errorf("%s changed while it was captured", path.Base(p))
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, in)
	if err == nil && n != looked.Size() {
		err = fmt.Errorf("%s changed while it was captured", path.Base(p))
	}
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// taskImages answers what was captured of a task's declared images, capturing
// them first if the task has exited and they were not yet. A released
// task's are gone.
func (s *Server) taskImages(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if s.releasing(key) {
		http.Error(w, execution.ErrImageGone.Error(), http.StatusGone)
		return
	}
	if _, found, err := s.readRecord(key); err != nil || !found {
		if err != nil {
			httpError(w, err)
			return
		}
		http.Error(w, "no such task", http.StatusNotFound)
		return
	}
	_, _, err := s.captureResults(r.Context(), key)
	switch {
	case errors.Is(err, errNotExited):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, execution.ErrImageGone):
		http.Error(w, err.Error(), http.StatusGone)
		return
	case err != nil:
		httpError(w, err)
		return
	}
	captures, _, err := s.readImages(key)
	if err != nil {
		httpError(w, err)
		return
	}
	if captures == nil {
		captures = map[string]execution.ImageCapture{}
	}
	writeJSON(w, captures)
}

// imageFile serves one file of a captured image: its index, or a blob. It
// is opened under the task's lock, which its release takes, so once a
// release is decided no file is opened; a file already open is read to its
// end, whatever the release removes meanwhile.
func (s *Server) imageFile(w http.ResponseWriter, r *http.Request) {
	key, name, file := r.PathValue("key"), r.PathValue("name"), r.PathValue("file")
	if !execution.ImageFilePattern.MatchString(file) {
		http.Error(w, "an image's index or one of its blobs is served, nothing else", http.StatusBadRequest)
		return
	}
	f, status, err := s.openImageFile(key, name, file)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		httpError(w, err)
		return
	}
	// The length is stated: a reply cut short is a transport error at
	// AppTown, never a shorter file.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	_, _ = io.Copy(w, f)
}

func (s *Server) openImageFile(key, name, file string) (*os.File, int, error) {
	unlock := s.lockTask(key)
	defer unlock()
	if s.releasing(key) {
		return nil, http.StatusGone, execution.ErrImageGone
	}
	captures, found, err := s.readImages(key)
	switch {
	case err != nil:
		return nil, http.StatusInternalServerError, err
	case !found:
		return nil, http.StatusConflict, errors.New("the task's images are not captured: read its images first")
	}
	// Only a declared name, never a path, names the folder; only an image
	// captured present has one.
	if _, ok := captures[name]; !ok {
		return nil, http.StatusNotFound, fmt.Errorf("the task declared no image %q", name)
	}
	f, err := os.Open(filepath.Join(s.taskImagesPath(key), name, filepath.FromSlash(file)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, http.StatusNotFound, fmt.Errorf("the image %s has no %s", name, file)
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return f, 0, nil
}
