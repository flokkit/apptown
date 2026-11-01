package worker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
)

// SessionClient is AppTown's side of a worker's sessions. Its client must
// present the installation's certificate.
//
// An answer AppTown cannot get or read is execution.ErrWorkerUnavailable:
// what the worker holds is then unknown, never absent. Only the worker's own
// answer says a session is gone (SessionStatus.Gone) or unknown to it.
type SessionClient struct {
	URL    string
	Client *http.Client
}

var (
	// errRemoteSessionRemoved: the worker removed the session, and will not start
	// it again.
	errRemoteSessionRemoved = errors.New("the worker removed this session")
	// errNoSession: the worker has no record of the session.
	errNoSession = errors.New("the worker has no such session")
	// errRemoteSessionBusy: the worker refused to rebuild a running chat.
	errRemoteSessionBusy = errors.New("the chat is busy")
)

// transport is the one worker transport (execution.WorkerClient).
func (c SessionClient) transport() execution.WorkerClient {
	return execution.WorkerClient{URL: c.URL, Client: c.Client}
}

// maxSessionAnswer bounds a session endpoint's JSON answer.
const maxSessionAnswer = 4 << 20

func (c SessionClient) call(ctx context.Context, method, path string, in, out any) error {
	return c.transport().Call(ctx, method, path, in, out, maxSessionAnswer, refusal)
}

// refusal classifies an answer that is not success: the worker's refusal,
// read by execution.WorkerRefusal (a 5xx, or a refusal that cannot be
// read, is the worker unavailable), with the session's own statuses named.
func refusal(resp *http.Response) error {
	err := execution.WorkerRefusal(resp)
	if errors.Is(err, execution.ErrWorkerUnavailable) {
		return err
	}
	switch resp.StatusCode {
	case http.StatusConflict:
		return fmt.Errorf("%w: %v", errRemoteSessionRemoved, err)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %v", errNoSession, err)
	case http.StatusLocked:
		return fmt.Errorf("%w: %v", errRemoteSessionBusy, err)
	}
	return err
}

func sessionPath(id, suffix string) string { return "/v1/sessions/" + url.PathEscape(id) + suffix }

// Put records the session on the worker, or replaces its spec.
func (c SessionClient) Put(ctx context.Context, id string, spec SessionSpec) (SessionStatus, error) {
	var st SessionStatus
	return st, c.call(ctx, http.MethodPut, sessionPath(id, ""), spec, &st)
}

// start makes the session's container and private daemon run.
func (c SessionClient) start(ctx context.Context, id string) (SessionStatus, error) {
	var st SessionStatus
	return st, c.call(ctx, http.MethodPost, sessionPath(id, "/start"), nil, &st)
}

// Status reports the session as the worker sees it now.
func (c SessionClient) Status(ctx context.Context, id string) (SessionStatus, error) {
	var st SessionStatus
	return st, c.call(ctx, http.MethodGet, sessionPath(id, ""), nil, &st)
}

// Stop stops the session's runtime; the session stays.
func (c SessionClient) Stop(ctx context.Context, id string) (SessionStatus, error) {
	var st SessionStatus
	return st, c.call(ctx, http.MethodPost, sessionPath(id, "/stop"), nil, &st)
}

// Rebuild removes the session's runtime; the next Start creates it anew
// from the session's spec. The session stays.
func (c SessionClient) Rebuild(ctx context.Context, id string) (SessionStatus, error) {
	var st SessionStatus
	return st, c.call(ctx, http.MethodPost, sessionPath(id, "/rebuild"), nil, &st)
}

// Remove ends the session for good. It succeeds only once the worker shows
// the session's runtime is gone; the worker never starts it again.
func (c SessionClient) Remove(ctx context.Context, id string) (SessionStatus, error) {
	var st SessionStatus
	return st, c.call(ctx, http.MethodDelete, sessionPath(id, ""), nil, &st)
}

// List lists the worker's sessions, removed ones included.
func (c SessionClient) List(ctx context.Context) ([]SessionSummary, error) {
	var out []SessionSummary
	return out, c.call(ctx, http.MethodGet, "/v1/sessions", nil, &out)
}

// send is call with a raw body, for tars.
func (c SessionClient) send(ctx context.Context, method, path string, body io.Reader, out any) error {
	return c.transport().Send(ctx, method, path, body, "application/x-tar", out, maxSessionAnswer, refusal)
}

// ensureBlob sends a blob (a tar with one entry named "content", named by
// its SHA-256) unless the worker has it.
func (c SessionClient) ensureBlob(ctx context.Context, sum string, tar []byte) error {
	return c.transport().EnsureBlob(ctx, sum, tar)
}

// putFiles replaces the session's files with the tar.
func (c SessionClient) putFiles(ctx context.Context, id string, tar io.Reader) (SessionStatus, error) {
	var st SessionStatus
	return st, c.send(ctx, http.MethodPut, sessionPath(id, "/files"), tar, &st)
}

// deliver types a message into the chat. An error means it may or may not
// have arrived.
func (c SessionClient) deliver(ctx context.Context, id, text string) error {
	var out struct{}
	return c.call(ctx, http.MethodPost, sessionPath(id, "/deliver"), DeliverRequest{Text: text}, &out)
}

// interrupt stops the chat's current turn.
func (c SessionClient) interrupt(ctx context.Context, id string) error {
	var out struct{}
	return c.call(ctx, http.MethodPost, sessionPath(id, "/interrupt"), nil, &out)
}

// records lists the chat's record files.
func (c SessionClient) records(ctx context.Context, id string) ([]RecordFile, error) {
	var out []RecordFile
	return out, c.call(ctx, http.MethodGet, sessionPath(id, "/records"), nil, &out)
}

// record reads a record file from an offset.
func (c SessionClient) record(ctx context.Context, id, name string, from int64) ([]byte, error) {
	resp, err := c.transport().Do(ctx, http.MethodGet, sessionPath(id, "/records/"+url.PathEscape(name))+"?from="+strconv.FormatInt(from, 10), nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, refusal(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRecordRead+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the record: %w", execution.ErrWorkerUnavailable, err)
	}
	return body, nil
}

// terminal opens the chat's terminal on the worker.
func (c SessionClient) terminal(ctx context.Context, id string, size console.Size) (console.Terminal, error) {
	// A terminal lasts as long as it is open: a client's overall timeout
	// would cut it, and would leave its connection unwritable.
	hc := http.Client{}
	if c.Client != nil {
		hc = *c.Client
	}
	hc.Timeout = 0
	resp, err := execution.WorkerClient{URL: c.URL, Client: &hc}.Do(ctx, http.MethodGet, fmt.Sprintf("%s?cols=%d&rows=%d", sessionPath(id, "/terminal"), size.Cols, size.Rows),
		nil, http.Header{"Connection": {"Upgrade"}, "Upgrade": {terminalProtocol}})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		return nil, refusal(resp)
	}
	rw, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, errors.New("the worker's terminal connection is not writable")
	}
	return &remoteTerminal{conn: rw}, nil
}

// remoteTerminal is the worker's terminal: screen bytes in, framed
// keystrokes and resizes out.
type remoteTerminal struct {
	conn io.ReadWriteCloser
	mu   sync.Mutex
}

func (t *remoteTerminal) Read(p []byte) (int, error) { return t.conn.Read(p) }

func (t *remoteTerminal) frame(kind byte, payload []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	head := make([]byte, 5, 5+len(payload))
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(payload)))
	_, err := t.conn.Write(append(head, payload...))
	return err
}

func (t *remoteTerminal) Write(p []byte) (int, error) {
	for rest := p; len(rest) > 0; {
		n := min(len(rest), maxFrame)
		if err := t.frame(frameKeys, rest[:n]); err != nil {
			return len(p) - len(rest), err
		}
		rest = rest[n:]
	}
	return len(p), nil
}

func (t *remoteTerminal) Resize(size console.Size) error {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload, size.Cols)
	binary.BigEndian.PutUint16(payload[2:], size.Rows)
	return t.frame(frameResize, payload)
}

func (t *remoteTerminal) Close() error { return t.conn.Close() }

// Reach asks the worker which of the addresses it can reach.
func (c SessionClient) Reach(ctx context.Context, urls []string) ([]Reach, error) {
	var out []Reach
	return out, c.call(ctx, http.MethodPost, "/v1/reach", ReachRequest{URLs: urls}, &out)
}

// putSource sends the bundle the chat's work tree is made from.
func (c SessionClient) putSource(ctx context.Context, id, branch, commit string, bundle io.Reader) (SessionStatus, error) {
	var st SessionStatus
	q := url.Values{"branch": {branch}, "commit": {commit}}
	return st, c.send(ctx, http.MethodPut, sessionPath(id, "/source?"+q.Encode()), bundle, &st)
}

// Save asks for a bundle of the chat's commits since base, writes it to
// dest, and answers the commit it holds; "" when there is nothing new. Only
// 204 means nothing new: a reply without its commit is not an answer. A
// non-empty leftovers commits what is uncommitted under that message first.
func (c SessionClient) Save(ctx context.Context, id, base, leftovers, dest string) (string, error) {
	q := url.Values{"base": {base}}
	if leftovers != "" {
		q.Set("leftovers", leftovers)
	}
	resp, err := c.transport().Do(ctx, http.MethodGet, sessionPath(id, "/save?"+q.Encode()), nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return "", nil
	}
	if resp.StatusCode >= 300 {
		return "", refusal(resp)
	}
	head := resp.Header.Get("AppTown-Head")
	if resp.StatusCode != http.StatusOK || !commitPattern.MatchString(head) {
		return "", fmt.Errorf("%w: the chat's save came back as %d naming commit %q", execution.ErrWorkerUnavailable, resp.StatusCode, head)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBundleBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("%w: receiving the chat's save: %w", execution.ErrWorkerUnavailable, err)
	}
	if n > maxBundleBytes {
		return "", fmt.Errorf("%w: the chat's save is larger than %d bytes", gitexec.ErrBundleRefused, maxBundleBytes)
	}
	return head, nil
}
