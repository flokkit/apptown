package worker

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/console"
)

// What travels to a session besides its spec:
//
//   - The chat's files: its settings, prompt and pictures, sent as one tar
//     whenever they change and written into the session's console directory.
//     The chat's own record (its transcript) and scratch are the worker's and
//     never overwritten.
//   - Blobs: content a chat mounts read-only (the forge's own `forge` binary,
//     generated registry configuration), sent once and named by the SHA-256
//     of the tar that carries it, so a mount names content, not a path.
//   - The work tree, made once from a bundle of the change's branch before
//     the chat first starts, by the worker's git with hooks off. A save
//     comes back as a bundle made by the chat's own git in its container.
//
// Messages, interrupts and the terminal go to the chat runtime; the chat's
// record is read back by the forge, file by file, from an offset.

const (
	maxFilesBytes = 64 << 20
	maxBlobBytes  = 512 << 20
	maxEntries    = 20000
	// maxRecordRead bounds one answer when the forge reads the record.
	maxRecordRead = 4 << 20
)

// workerOwned are the console directory's names the worker and the chat
// write; the forge's files never replace them.
var workerOwned = []string{"sessions", "scratch", "docker-client", "docker-runtime", "run-pi.sh", "tmux.conf", filesManifest}

const filesManifest = "forge-files.json"

// blobContent is where a blob's content is: a file or a directory.
func (s *Server) blobContent(sum string) string {
	return filepath.Join(s.State, "blobs", sum, "content")
}

// headBlob answers whether a blob has been sent.
func (s *Server) headBlob(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat(s.blobContent(r.PathValue("sum"))); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// putBlob receives a tar holding one entry named "content" (a file, or a
// directory of files) and keeps it under the SHA-256 of the tar. A body
// whose digest is not its name is refused. Sending the same blob again
// changes nothing.
func (s *Server) putBlob(w http.ResponseWriter, r *http.Request) {
	sum := r.PathValue("sum")
	if _, err := os.Stat(s.blobContent(sum)); err == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	dir := filepath.Join(s.State, "blobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	spool, err := os.CreateTemp(dir, "upload-*.tar")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(spool, h), io.LimitReader(r.Body, maxBlobBytes+1))
	if err != nil {
		http.Error(w, "reading the blob: "+err.Error(), http.StatusBadRequest)
		return
	}
	if n > maxBlobBytes {
		http.Error(w, "the blob is too large", http.StatusRequestEntityTooLarge)
		return
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		http.Error(w, "the blob's digest is not its name", http.StatusBadRequest)
		return
	}
	tmp, err := os.MkdirTemp(dir, "extract-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmp)
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	root, err := os.OpenRoot(tmp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	names, err := extract(spool, root, false)
	root.Close()
	if err != nil {
		http.Error(w, "unpacking the blob: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(names) == 0 || slices.ContainsFunc(names, func(n string) bool { return n != "content" && !strings.HasPrefix(n, "content/") }) {
		http.Error(w, `a blob holds one entry named "content"`, http.StatusBadRequest)
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, sum)); err != nil && !errors.Is(err, os.ErrExist) {
		if _, statErr := os.Stat(s.blobContent(sum)); statErr != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusCreated)
}

// putFiles replaces the chat's files with the tar sent: names the previous
// tar placed and this one does not are removed; the worker's own names are
// refused. The record keeps the tar's digest, so the forge sends files only
// when they change.
func (s *Server) putFiles(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	id := r.PathValue("id")
	defer s.lockSession(id)()
	rec, ok := s.knownSession(w, id)
	if !ok {
		return
	}
	dir := s.launch(rec).Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	spool, err := os.CreateTemp(s.sessionFiles(id), "files-*.tar")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(spool, h), io.LimitReader(r.Body, maxFilesBytes+1))
	if err != nil || n > maxFilesBytes {
		http.Error(w, "the chat's files are unreadable or too large", http.StatusBadRequest)
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Checked before anything is written: a tar naming the worker's own
	// files changes nothing.
	if err := checkNames(tar.NewReader(spool)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Every change is made through a root on the chat's directory: the
	// chat can change links under its writable folders at any moment, and
	// a root refuses to follow one out of the directory while it acts.
	root, err := os.OpenRoot(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer root.Close()
	var previous []string
	if raw, err := root.ReadFile(filesManifest); err == nil {
		_ = json.Unmarshal(raw, &previous)
	}
	names, err := extract(spool, root, true)
	if err != nil {
		http.Error(w, "writing the chat's files: "+err.Error(), http.StatusBadRequest)
		return
	}
	for _, old := range previous {
		if !slices.Contains(names, old) && localName(old) {
			_ = root.Remove(old)
		}
	}
	raw, _ := json.Marshal(names)
	if err := root.WriteFile(filesManifest, raw, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rec.FilesDigest = hex.EncodeToString(h.Sum(nil))
	if err := s.writeSession(rec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.answerSession(w, r.Context(), rec, true)
}

func checkNames(tr *tar.Reader) error {
	links := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if !localName(name) {
			return fmt.Errorf("%q is not a name inside the chat's files", hdr.Name)
		}
		top := strings.SplitN(name, "/", 2)[0]
		if slices.Contains(workerOwned, top) {
			return fmt.Errorf("%q is the worker's, not AppTown's", hdr.Name)
		}
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			if links[dir] {
				return fmt.Errorf("%q passes through a symbolic link", hdr.Name)
			}
		}
		if hdr.Typeflag == tar.TypeSymlink {
			links[name] = true
		}
	}
}

func localName(name string) bool {
	return name != "" && filepath.IsLocal(name) && filepath.Clean(name) == name
}

// extract writes a tar's regular files and directories under root, and
// its symbolic links when links is set; nothing else. Every write goes
// through root, which will not follow a link out of it, whoever made the
// link and whenever. It returns the names written.
func extract(r io.Reader, root *os.Root, links bool) ([]string, error) {
	return extractBounded(r, root, links, maxBlobBytes, maxEntries)
}

// extractBounded is extract with its own bounds on bytes and entries.
func extractBounded(r io.Reader, root *os.Root, links bool, maxBytes int64, maxEntries int) ([]string, error) {
	tr := tar.NewReader(r)
	var names []string
	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return names, err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if !localName(name) {
			return names, fmt.Errorf("%q is not a local name", hdr.Name)
		}
		if entries++; entries > maxEntries {
			return names, errors.New("too many entries")
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return names, err
			}
			continue
		case tar.TypeReg:
			total += hdr.Size
			if total > maxBytes {
				return names, errors.New("too large")
			}
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return names, err
			}
			_ = root.Remove(name)
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return names, err
			}
			_, err = io.Copy(f, tr)
			if err = errors.Join(err, f.Close()); err != nil {
				return names, err
			}
		case tar.TypeSymlink:
			if !links {
				return names, fmt.Errorf("%q: symbolic links are not accepted here", hdr.Name)
			}
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return names, err
			}
			_ = root.Remove(name)
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return names, err
			}
		default:
			return names, fmt.Errorf("%q: only files, directories and links", hdr.Name)
		}
		names = append(names, name)
	}
}

// putSource makes the chat's work tree from a bundle of the change's
// branch, once, before the chat first runs. The worker's git clones the
// forge's bundle into a new directory with hooks and configuration off; the
// work tree is then the chat's, and the worker's git never runs in it
// again. Sending the same commit again changes nothing; another commit is
// refused.
func (s *Server) putSource(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	id := r.PathValue("id")
	branch, commit := r.URL.Query().Get("branch"), r.URL.Query().Get("commit")
	if !branchPattern.MatchString(branch) || strings.Contains(branch, "..") || !commitPattern.MatchString(commit) {
		http.Error(w, "a source needs its branch and commit", http.StatusBadRequest)
		return
	}
	defer s.lockSession(id)()
	rec, ok := s.knownSession(w, id)
	if !ok {
		return
	}
	if rec.Source != "" {
		if rec.Source != commit {
			http.Error(w, "this chat's work tree was made from another commit", http.StatusConflict)
			return
		}
		s.answerSession(w, r.Context(), rec, true)
		return
	}
	files := s.sessionFiles(id)
	work := filepath.Join(files, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries, err := os.ReadDir(work); err != nil || len(entries) > 0 {
		http.Error(w, "the chat's work tree is not empty", http.StatusConflict)
		return
	}
	spool, err := os.CreateTemp(files, "source-*.bundle")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(spool.Name())
	n, err := io.Copy(spool, io.LimitReader(r.Body, MaxBundleBytes+1))
	if cerr := spool.Close(); err == nil {
		err = cerr
	}
	if err != nil || n > MaxBundleBytes {
		http.Error(w, "the source bundle is unreadable or too large", http.StatusBadRequest)
		return
	}
	tmp, err := os.MkdirTemp(files, "source-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmp)
	if out, err := s.runGit(r.Context(), "", "clone", "-q", "--branch", branch, spool.Name(), tmp); err != nil {
		http.Error(w, "making the work tree from the bundle: "+out, http.StatusUnprocessableEntity)
		return
	}
	if head, err := s.runGit(r.Context(), tmp, "rev-parse", "--verify", "HEAD^{commit}"); err != nil || head != commit {
		http.Error(w, "the bundle does not hold the named commit", http.StatusUnprocessableEntity)
		return
	}
	// The work tree has no remote: its commits leave only as bundles the
	// forge asks for.
	_, _ = s.runGit(r.Context(), tmp, "remote", "remove", "origin")
	// The chat commits as a step's agent does (seedAgentGit): without an
	// identity its first commit fails until it sets one.
	for _, kv := range [][2]string{{"user.name", "forge"}, {"user.email", "forge@localhost"}} {
		if out, err := s.runGit(r.Context(), tmp, "config", kv[0], kv[1]); err != nil {
			http.Error(w, "giving the work tree its commit identity: "+out, http.StatusInternalServerError)
			return
		}
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(tmp, e.Name()), filepath.Join(work, e.Name())); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	rec.Source = commit
	if err := s.writeSession(rec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.answerSession(w, r.Context(), rec, true)
}

// chatScript runs a script on a chat's work tree in a short-lived
// container from the chat's image (pi.Runtime on a worker).
type chatScript interface {
	Script(ctx context.Context, in console.Launch, script string, env []string, stdout io.Writer) error
}

// saveScript runs on the chat's work tree with the chat's own tools, in a
// container of the chat's image, whether or not the chat runs: it commits
// what is left uncommitted when LEFTOVERS names the commit (the chat's end),
// names the chat's current commit, and bundles the commits since BASE. It
// exits 3 when there is nothing new. The worker's git never reads the
// chat's repository. What it stages follows the forge's own rule for a
// change's folder: never the .forge runtime, always .forge/app.yaml.
const saveScript = `set -e
if [ -n "$LEFTOVERS" ] && [ -n "$(git status --porcelain)" ]; then
  git add -A -- ':(exclude).forge'
  if [ -e .forge/app.yaml ] || [ -L .forge/app.yaml ] || git ls-files --error-unmatch -- .forge/app.yaml >/dev/null 2>&1; then
    git add -f -A -- .forge/app.yaml
  fi
  if ! git diff --cached --quiet; then
    git -c user.name=forge -c user.email=forge@localhost commit -q -m "$LEFTOVERS"
  fi
fi
head=$(git rev-parse --verify HEAD^{commit})
[ "$head" = "$BASE" ] && exit 3
git update-ref refs/forge/save "$head"
git bundle create -q - refs/forge/save "^$BASE"
`

// SaveRef is the ref a save's bundle names its tip by.
const SaveRef = "refs/forge/save"

// save answers a bundle of the chat's commits since base, made with the
// chat's own tools, with the tip it holds in Forge-Head; 204 when there is
// nothing new. With leftovers set (the chat's end, once it has stopped),
// uncommitted work is first committed under that message. What it sends is
// the chat's and untrusted: the forge imports it through its checked
// import.
func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	ex, ok := s.Sessions.(chatScript)
	if !ok {
		http.Error(w, "this worker cannot save chats", http.StatusNotImplemented)
		return
	}
	base, leftovers := r.URL.Query().Get("base"), r.URL.Query().Get("leftovers")
	if !commitPattern.MatchString(base) || len(leftovers) > 200 || strings.ContainsAny(leftovers, "\n\x00") {
		http.Error(w, "a save needs the commit it builds on", http.StatusBadRequest)
		return
	}
	in, ok := s.liveLaunch(w, r.PathValue("id"))
	if !ok {
		return
	}
	spool, err := os.CreateTemp(s.sessionFiles(in.ID), "save-*.bundle")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	limited := &limitWriter{w: spool, n: MaxBundleBytes}
	err = ex.Script(r.Context(), in, saveScript, []string{"BASE=" + base, "LEFTOVERS=" + leftovers}, limited)
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if limited.over {
		http.Error(w, "the chat's save is larger than the worker sends", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "making the chat's save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	head, err := bundleTip(spool, SaveRef)
	if err != nil {
		http.Error(w, "the chat's save names no commit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Forge-Head", head)
	_, _ = io.Copy(w, spool)
}

// bundleTip reads the commit a bundle's header names for ref.
func bundleTip(f io.Reader, ref string) (string, error) {
	rd := bufio.NewReader(io.LimitReader(f, 64<<10))
	first, err := rd.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "# v") {
		return "", errors.New("not a bundle")
	}
	for {
		line, err := rd.ReadString('\n')
		if err != nil || line == "\n" {
			return "", errors.New("no " + ref)
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref && commitPattern.MatchString(fields[0]) {
			return fields[0], nil
		}
	}
}

// limitWriter writes at most n bytes, and then refuses.
type limitWriter struct {
	w    io.Writer
	n    int64
	over bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		l.over = true
		return 0, errors.New("too large")
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}

// DeliverRequest is a message for the chat.
type DeliverRequest struct {
	Text string `json:"text"`
}

// liveLaunch reads a live session's launch under its lock, and lets go of
// the lock before the caller acts: a message or a terminal does not hold
// off a stop or a removal, which ends it instead.
func (s *Server) liveLaunch(w http.ResponseWriter, id string) (console.Launch, bool) {
	unlock := s.lockSession(id)
	defer unlock()
	rec, ok := s.knownSession(w, id)
	if !ok {
		return console.Launch{}, false
	}
	return s.launch(rec), true
}

// deliver types a message into the chat and confirms it entered the chat's
// record. A failure means the message may or may not have arrived.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	var req DeliverRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "a message needs text", http.StatusBadRequest)
		return
	}
	in, ok := s.liveLaunch(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.Sessions.Deliver(r.Context(), in, req.Text); err != nil {
		http.Error(w, "delivering the message: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct{}{})
}

func (s *Server) interrupt(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	in, ok := s.liveLaunch(w, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.Sessions.Interrupt(r.Context(), in.ID); err != nil {
		http.Error(w, "interrupting: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct{}{})
}

// RecordFile is one of the chat's record files, its size now and when it
// last changed: the forge keeps the time on its copy, so the record's quiet
// (a turn handed back) reads the same there.
type RecordFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

var recordPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}\.jsonl$`)

// records lists the chat's record files: what the forge copies to show the
// conversation.
func (s *Server) records(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	in, ok := s.liveLaunch(w, r.PathValue("id"))
	if !ok {
		return
	}
	out := []RecordFile{}
	root, err := os.OpenRoot(filepath.Join(in.Dir, "sessions"))
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, out)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer root.Close()
	d, err := root.Open(".")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || !recordPattern.MatchString(e.Name()) {
			continue
		}
		out = append(out, RecordFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	writeJSON(w, out)
}

// record answers one record file's bytes from an offset, at most
// maxRecordRead of them.
func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	name := r.PathValue("name")
	from, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if !recordPattern.MatchString(name) || err != nil || from < 0 {
		http.Error(w, "a record file and an offset", http.StatusBadRequest)
		return
	}
	in, ok := s.liveLaunch(w, r.PathValue("id"))
	if !ok {
		return
	}
	// Read through a root on the record's directory: the chat writes it,
	// and a link there must not show the forge a file of the worker's. A
	// missing record file is not a missing session, so it is not a 404.
	root, err := os.OpenRoot(filepath.Join(in.Dir, "sessions"))
	if err != nil {
		http.Error(w, "no such record file", http.StatusUnprocessableEntity)
		return
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		http.Error(w, "no such record file", http.StatusUnprocessableEntity)
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		http.Error(w, "no such record file", http.StatusUnprocessableEntity)
		return
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, io.LimitReader(f, maxRecordRead))
}

// The terminal is passed through on an upgraded connection. The worker
// sends the screen's bytes as they come; the forge sends frames: one type
// byte, a four-byte big-endian length, then the payload. Type 0 is
// keystrokes; type 1 is a resize, two big-endian uint16s (columns, rows).
const (
	terminalProtocol = "forge-terminal"
	frameKeys        = 0
	frameResize      = 1
	maxFrame         = 1 << 20
)

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	if !s.sessionRuntime(w) {
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), terminalProtocol) {
		http.Error(w, "the terminal needs an upgraded connection", http.StatusBadRequest)
		return
	}
	in, ok := s.liveLaunch(w, r.PathValue("id"))
	if !ok {
		return
	}
	cols, _ := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 16)
	rows, _ := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 16)
	term, err := s.Sessions.Attach(r.Context(), in.ID, console.Size{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		http.Error(w, "opening the terminal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer term.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "this connection cannot be upgraded", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + terminalProtocol + "\r\nConnection: Upgrade\r\n\r\n")
	if buf.Flush() != nil {
		return
	}
	// Whichever side ends first ends the other: the chat's screen closing
	// closes the connection, which ends the forge's reading and its frames.
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(conn, term)
		_ = conn.Close()
	}()
	_ = readFrames(buf.Reader, term)
	_ = term.Close()
	_ = conn.Close()
	<-copied
}

// readFrames applies the forge's frames to the terminal until the
// connection ends.
func readFrames(r *bufio.Reader, term console.Terminal) error {
	var head [5]byte
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(head[1:])
		if n > maxFrame {
			return errors.New("frame too large")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return err
		}
		switch head[0] {
		case frameKeys:
			if _, err := term.Write(payload); err != nil {
				return err
			}
		case frameResize:
			if len(payload) == 4 {
				_ = term.Resize(console.Size{Cols: binary.BigEndian.Uint16(payload), Rows: binary.BigEndian.Uint16(payload[2:])})
			}
		}
	}
}
