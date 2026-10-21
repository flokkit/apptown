package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console"
)

// RemoteAgent is a chat runtime on a worker, for AppTown's chat service:
// the console.Agent the service calls, answered by a worker's sessions
// (design/operations/workers.md, "Chats on the worker").
// The chat service is unchanged; only where the agent runs moves.
//
// What AppTown would mount into a chat's container travels as blobs,
// each sent once and named by its content, so AppTown's `apptown` binary
// and the project's generated registry configuration reach the chat without
// any path on either machine. The chat's other files (settings, prompt,
// pictures) are sent when they change. The chat's record is copied back to
// the session's directory on AppTown, with the time of the last good copy
// (RecordAsOf), so the conversation reads as it does for a chat on the
// forge. Whether a rebuild is safe is the worker's answer, never the copy's.
type RemoteAgent struct {
	Sessions SessionClient
	// Image is the agent image when a launch names none.
	Image string
	// Root is the directory holding AppTown's session directories
	// (<root>/<id>), where each chat's record is copied back to.
	Root string
	// Source writes a bundle of the change's branch for a session's work
	// tree to dest and names its branch and commit. It is asked once, before
	// the chat first starts; nil sends none.
	Source func(ctx context.Context, in console.Launch, dest string) (branch, commit string, err error)
	// CopyEvery is how often a started chat's record is copied back; zero
	// means two seconds.
	CopyEvery time.Duration
	// Ending, when set, makes the last save of an ending chat, after its
	// runtime has stopped and before it is removed (SaveLast answers that
	// save's bundle). An error keeps the chat: its end is asked again.
	Ending func(ctx context.Context, id string) error
	// HandedBack, when set, is called after a copy in which AppTown's
	// copy of the record shows the agent has handed back a turn it had not
	// shown before: the moment to save. It is a trigger, never proof that
	// the chat has stopped working.
	HandedBack func(id string)
	// Now is AppTown's clock, which stamps a good copy's time
	// (RecordAsOf). Nil is the real clock (clock.Or).
	Now func() time.Time

	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	loops  sync.WaitGroup // the copy loops, waited for by Close
	mu     sync.Mutex
	sent   map[string]bool          // blobs the worker is known to have
	copies map[string]chan struct{} // sessions whose record is being copied
	copyMu map[string]*sync.Mutex   // one copy at a time per session
}

var _ console.Agent = (*RemoteAgent)(nil)

// RecordAsOfFile, in a session's directory on AppTown, holds the time of
// the last good copy of the chat's record from its worker.
const RecordAsOfFile = "record-as-of"

func (a *RemoteAgent) init() {
	a.once.Do(func() {
		a.ctx, a.cancel = context.WithCancel(context.Background())
		a.sent, a.copies, a.copyMu = map[string]bool{}, map[string]chan struct{}{}, map[string]*sync.Mutex{}
	})
}

// Close stops copying records, and waits for any copy (and the save it
// may have started) to finish. Sessions on the worker are untouched.
func (a *RemoteAgent) Close() {
	a.init()
	a.cancel()
	a.loops.Wait()
}

// spec is the worker's form of a launch: mounts become blobs the worker
// has, and nothing names a path on AppTown.
func (a *RemoteAgent) spec(ctx context.Context, in console.Launch) (SessionSpec, error) {
	image := in.Image
	if image == "" {
		image = a.Image
	}
	spec := SessionSpec{Image: image, Model: in.Model, AgentSession: in.AgentSession, Docker: in.Docker, Mirror: in.Mirror, Env: slices.Clone(in.Env)}
	for _, m := range in.Mounts {
		parts := strings.Split(m, ":")
		if len(parts) != 3 || parts[2] != "ro" || !filepath.IsAbs(parts[0]) {
			return SessionSpec{}, fmt.Errorf("a chat on a worker can only have read-only files mounted, not %q", m)
		}
		sum, err := a.sendBlob(ctx, parts[0])
		if err != nil {
			return SessionSpec{}, fmt.Errorf("sending %s to the worker: %w", parts[1], err)
		}
		spec.Mounts = append(spec.Mounts, SessionMount{Blob: sum, Target: parts[1]})
	}
	return spec, nil
}

// sendBlob sends a file or directory as a blob, unless the worker has it.
func (a *RemoteAgent) sendBlob(ctx context.Context, path string) (string, error) {
	body, err := blobTar(path)
	if err != nil {
		return "", err
	}
	raw := sha256.Sum256(body)
	sum := hex.EncodeToString(raw[:])
	a.mu.Lock()
	sent := a.sent[sum]
	a.mu.Unlock()
	if sent {
		return sum, nil
	}
	has, err := a.Sessions.HasBlob(ctx, sum)
	if err != nil {
		return "", err
	}
	if !has {
		if err := a.Sessions.PutBlob(ctx, sum, bytes.NewReader(body)); err != nil {
			return "", err
		}
	}
	a.mu.Lock()
	a.sent[sum] = true
	a.mu.Unlock()
	return sum, nil
}

// Start sends the chat's spec, then its files if they changed, and starts
// it on the worker; the chat's record is copied back from then on.
func (a *RemoteAgent) Start(ctx context.Context, in console.Launch) error {
	a.init()
	spec, err := a.spec(ctx, in)
	if err != nil {
		return err
	}
	st, err := a.Sessions.Put(ctx, in.ID, spec)
	if err != nil {
		return err
	}
	files, err := filesTar(in.Dir)
	if err != nil {
		return err
	}
	raw := sha256.Sum256(files)
	if hex.EncodeToString(raw[:]) != st.FilesDigest {
		if _, err := a.Sessions.PutFiles(ctx, in.ID, bytes.NewReader(files)); err != nil {
			return err
		}
	}
	if st.Source == "" && a.Source != nil {
		if err := a.sendSource(ctx, in); err != nil {
			return err
		}
	}
	if _, err := a.Sessions.Start(ctx, in.ID); err != nil {
		return err
	}
	a.copyRecord(in.ID)
	return nil
}

// sendSource sends the change's branch for the chat's work tree.
func (a *RemoteAgent) sendSource(ctx context.Context, in console.Launch) error {
	dir, err := os.MkdirTemp("", "apptown-chat-source-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "source.bundle")
	branch, commit, err := a.Source(ctx, in, dest)
	if err != nil {
		return fmt.Errorf("bundling the chat's source: %w", err)
	}
	f, err := os.Open(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = a.Sessions.PutSource(ctx, in.ID, branch, commit, f)
	return err
}

// Save writes a bundle of the chat's commits since base to dest, made with
// the chat's own tools on the worker, and answers the commit it holds; ""
// when there is nothing new. The bundle is the chat's and untrusted: import
// it with gitexec.Quarantine, naming SaveRef.
func (a *RemoteAgent) Save(ctx context.Context, id, base, dest string) (string, error) {
	return a.Sessions.Save(ctx, id, base, "", dest)
}

// SaveLast is Save for an ending chat, once it has stopped: what it left
// uncommitted is committed first, as its leftovers.
func (a *RemoteAgent) SaveLast(ctx context.Context, id, base, dest string) (string, error) {
	return a.Sessions.Save(ctx, id, base, "Leftovers of chat "+id+" when it ended", dest)
}

// Stale sends the launch as the session's spec and answers the worker's
// view: stale, and safe to rebuild by the worker's own record.
func (a *RemoteAgent) Stale(ctx context.Context, in console.Launch) (bool, error) {
	a.init()
	spec, err := a.spec(ctx, in)
	if err != nil {
		return false, err
	}
	st, err := a.Sessions.Put(ctx, in.ID, spec)
	if err != nil {
		return false, err
	}
	return st.Stale, nil
}

// Status is the worker's answer. A session the worker does not know, or
// has removed, has no runtime; an unanswered question is an error, which
// the chat service shows as unavailable.
func (a *RemoteAgent) Status(ctx context.Context, id string) (console.Status, error) {
	st, err := a.Sessions.Status(ctx, id)
	if err != nil {
		return console.Status{}, err
	}
	return console.Status{Exists: st.Agent.Exists, Running: st.Agent.Running, EverStarted: st.Agent.EverStarted,
		Attached: st.Agent.Attached, SidecarExists: st.Docker.Exists, SidecarRunning: st.Docker.Running, ScriptExists: st.Script.Exists}, nil
}

// ErrNotConfirmed: AppTown could not confirm a message reached the chat.
// It may or may not have; the person decides whether to send it again.
var ErrNotConfirmed = errors.New("not confirmed: the message may or may not have reached the chat")

func (a *RemoteAgent) Deliver(ctx context.Context, in console.Launch, text string) error {
	a.init()
	if err := a.Sessions.Deliver(ctx, in.ID, text); err != nil {
		return fmt.Errorf("%w (%w)", ErrNotConfirmed, err)
	}
	a.copyNow(in)
	return nil
}

func (a *RemoteAgent) Interrupt(ctx context.Context, id string) error {
	return a.Sessions.Interrupt(ctx, id)
}

func (a *RemoteAgent) Attach(ctx context.Context, id string, size console.Size) (console.Terminal, error) {
	return a.Sessions.Terminal(ctx, id, size)
}

func (a *RemoteAgent) Stop(ctx context.Context, id string) error {
	_, err := a.Sessions.Stop(ctx, id)
	if errors.Is(err, ErrNoSession) {
		return nil
	}
	return err
}

// Remove rebuilds: the worker takes the runtime away and keeps the session.
func (a *RemoteAgent) Remove(ctx context.Context, id string) error {
	_, err := a.Sessions.Rebuild(ctx, id)
	if errors.Is(err, ErrNoSession) {
		return nil
	}
	return err
}

// End ends a chat on the worker, in this order:
//  1. its runtime stops (its turn, its terminal, anything it started, and
//     its private Docker daemon), so nothing writes its work tree;
//  2. its last save is made (Ending);
//  3. its record is copied one last time;
//  4. the session is removed for good.
//
// It succeeds only once the worker shows nothing of the chat; a worker that
// cannot be reached, or a last save not confirmed, leaves the chat unended,
// and its end is asked again.
func (a *RemoteAgent) End(ctx context.Context, id string) error {
	a.init()
	a.stopCopying(id)
	if _, err := a.Sessions.Stop(ctx, id); err != nil && !errors.Is(err, ErrNoSession) && !errors.Is(err, ErrSessionRemoved) {
		return fmt.Errorf("stopping the chat before its last save: %w", err)
	}
	if a.Ending != nil {
		if err := a.Ending(ctx, id); err != nil {
			return fmt.Errorf("the chat's last save: %w", err)
		}
	}
	if err := a.copyOnce(ctx, id); err != nil {
		// Only the worker saying the session is unknown or already removed
		// excuses the last copy; a failed or incomplete copy never does.
		if !errors.Is(err, ErrNoSession) && !errors.Is(err, ErrSessionRemoved) {
			return fmt.Errorf("copying the chat's record before it ends: %w", err)
		}
		st, serr := a.Sessions.Status(ctx, id)
		if serr != nil {
			return serr
		}
		if st.Known && !st.Removed {
			return fmt.Errorf("copying the chat's record before it ends: %w", err)
		}
	}
	_, err := a.Sessions.Remove(ctx, id)
	return err
}

// Reap resumes copying (and so saving after each turn) for AppTown's live
// chats, and ends sessions the worker holds that AppTown has no live chat
// for. A chat outlives an AppTown restart, and its turns go on being saved
// without anyone opening it. Copying does not wait on the worker's
// inventory: each copy retries until the worker answers, and stops once the
// worker says it has no such session.
func (a *RemoteAgent) Reap(ctx context.Context, alive map[string]bool) error {
	a.init()
	for id := range alive {
		a.copyRecord(id)
	}
	list, err := a.Sessions.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range list {
		if !s.Removed && !alive[s.ID] {
			if _, err := a.Sessions.Remove(ctx, s.ID); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// --- copying the chat's record back -------------------------------------------

// copyRecord starts copying a session's record back, once per session.
func (a *RemoteAgent) copyRecord(id string) {
	a.mu.Lock()
	if _, running := a.copies[id]; running {
		a.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	a.copies[id] = stop
	a.mu.Unlock()
	every := a.CopyEvery
	if every == 0 {
		every = 2 * time.Second
	}
	a.loops.Add(1)
	go func() {
		defer a.loops.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		var turns handedBackTurns
		for {
			ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
			err := a.copyOnce(ctx, id)
			cancel()
			if errors.Is(err, ErrNoSession) || errors.Is(err, ErrSessionRemoved) {
				a.stopCopying(id)
				return
			}
			if err == nil && a.HandedBack != nil && turns.next(&console.Transcript{Dir: filepath.Join(a.Root, id)}) {
				a.HandedBack(id)
			}
			select {
			case <-a.ctx.Done():
				return
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
}

// handedBackTurns reports each handed-back turn in a session's record
// once. A turn is the record's position (its newest file and size) when it
// reads handed back, so the same turn copied again is not reported again,
// and the next turn, which grows the record, is.
type handedBackTurns struct{ last string }

// next says whether tr shows a handed-back turn not reported before.
func (h *handedBackTurns) next(tr *console.Transcript) bool {
	if !tr.TurnHandedBack() {
		return false
	}
	file, size := tr.LatestSessionFile()
	at := fmt.Sprintf("%s@%d", file, size)
	if at == h.last {
		return false
	}
	h.last = at
	return true
}

// copyNow copies at once, so a delivered message shows without waiting.
func (a *RemoteAgent) copyNow(in console.Launch) {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	_ = a.copyOnce(ctx, in.ID)
}

func (a *RemoteAgent) stopCopying(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if stop, ok := a.copies[id]; ok {
		close(stop)
		delete(a.copies, id)
	}
}

// copyOnce brings the session's record files up to the worker's sizes:
// bytes past AppTown's copy are appended; a file that shrank on the
// worker is copied again whole. Then RecordAsOfFile is written.
func (a *RemoteAgent) copyOnce(ctx context.Context, id string) error {
	dir := filepath.Join(a.Root, id)
	a.mu.Lock()
	m := a.copyMu[id]
	if m == nil {
		m = &sync.Mutex{}
		a.copyMu[id] = m
	}
	a.mu.Unlock()
	m.Lock()
	defer m.Unlock()
	asOf := clock.Or(a.Now)().UTC()
	files, err := a.Sessions.Records(ctx, id)
	if err != nil {
		return err
	}
	local := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(local, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		if !recordPattern.MatchString(f.Name) {
			continue
		}
		path := filepath.Join(local, f.Name)
		var have int64
		if fi, err := os.Stat(path); err == nil {
			have = fi.Size()
		}
		if have > f.Size {
			if err := os.Remove(path); err != nil {
				return err
			}
			have = 0
		}
		for have < f.Size {
			chunk, err := a.Sessions.Record(ctx, id, f.Name, have)
			if err != nil {
				return err
			}
			if len(chunk) == 0 {
				// The file shrank since it was listed: this copy is not
				// complete, and its time is not recorded.
				return fmt.Errorf("the record %s changed while it was copied", f.Name)
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, err = out.Write(chunk)
			if err = errors.Join(err, out.Close()); err != nil {
				return err
			}
			have += int64(len(chunk))
		}
		if !f.Modified.IsZero() && have == f.Size {
			// The copy keeps the worker's time: whether the record has been
			// quiet (a turn handed back) reads the same on both machines.
			if err := os.Chtimes(path, f.Modified, f.Modified); err != nil {
				return err
			}
		}
	}
	// Written aside and renamed into place: a reader between a rewrite's
	// truncation and its write would read the chat as never copied. The
	// .tmp name stays on AppTown (filesTar leaves it out); the copy's
	// lock keeps one writer.
	tmp := filepath.Join(dir, RecordAsOfFile+".tmp")
	if err := os.WriteFile(tmp, []byte(asOf.Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, RecordAsOfFile))
}

// RecordAsOf reads when a chat's record was last copied from its worker,
// from its session directory on AppTown; zero when never.
func RecordAsOf(dir string) time.Time {
	raw, err := os.ReadFile(filepath.Join(dir, RecordAsOfFile))
	if err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
	return t
}

// --- tars ------------------------------------------------------------------------

// forgeOnly are the session directory's names that stay on AppTown: the
// record with its credential, and AppTown's copy of the chat's record.
var forgeOnly = []string{console.MetaFile, console.EndedFile, RecordAsOfFile}

// filesTar is the chat's files as the worker receives them: the session
// directory without the worker's own names or AppTown's. Names are
// sorted and times left out, so the same files make the same tar.
func filesTar(dir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		top := strings.SplitN(rel, "/", 2)[0]
		if slices.Contains(workerOwned, top) || slices.Contains(forgeOnly, top) || strings.HasSuffix(top, ".tmp") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return addEntry(tw, path, rel, d, true)
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// blobTar is a file or directory as a blob: one entry named "content".
// Symbolic links inside a directory are refused; a path that is itself a
// link is followed.
func blobTar(path string) ([]byte, error) {
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		name := "content"
		if rel != "." {
			name += "/" + filepath.ToSlash(rel)
		}
		return addEntry(tw, p, name, d, false)
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func addEntry(tw *tar.Writer, path, name string, d fs.DirEntry, links bool) error {
	switch {
	case d.Type()&fs.ModeSymlink != 0:
		if !links {
			return fmt.Errorf("%s is a symbolic link", path)
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777})
	case d.IsDir():
		return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755})
	case d.Type().IsRegular():
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: info.Size()}); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	}
	return nil // sockets and devices are not files a chat is given
}
