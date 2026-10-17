package worker

import (
	"bytes"
	"context"
	"encoding/binary"
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

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/gitexec"
)

// SessionClient is the forge's side of a worker's sessions. Its client must
// present the installation's certificate.
//
// An answer the forge cannot get or read is execution.ErrWorkerUnavailable:
// what the worker holds is then unknown, never absent. Only the worker's own
// answer says a session is gone (SessionStatus.Gone) or unknown to it.
type SessionClient struct {
	URL    string
	Client *http.Client
}

var (
	// ErrSessionRemoved: the worker removed the session, and will not start
	// it again.
	ErrSessionRemoved = errors.New("the worker removed this session")
	// ErrNoSession: the worker has no record of the session.
	ErrNoSession = errors.New("the worker has no such session")
	// ErrSessionBusy: the worker refused to rebuild a running chat.
	ErrSessionBusy = errors.New("the chat is busy")
)

func (c SessionClient) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", execution.ErrWorkerUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return refusal(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("%w: reading its answer: %w", execution.ErrWorkerUnavailable, err)
	}
	return nil
}

func (c SessionClient) client() *http.Client {
	if c.Client == nil {
		return http.DefaultClient
	}
	return c.Client
}

// refusal classifies an answer that is not success: the worker's refusal,
// or (5xx) an answer that says nothing certain.
func refusal(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(msg))
	switch {
	case resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrSessionRemoved, text)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNoSession, text)
	case resp.StatusCode == http.StatusLocked:
		return fmt.Errorf("%w: %s", ErrSessionBusy, text)
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: %s: %s", execution.ErrWorkerUnavailable, resp.Status, text)
	}
	return fmt.Errorf("the worker refused: %s: %s", resp.Status, text)
}

func sessionPath(id, suffix string) string { return "/v1/sessions/" + url.PathEscape(id) + suffix }

// Put records the session on the worker, or replaces its spec.
func (c SessionClient) Put(ctx context.Context, id string, spec SessionSpec) (SessionStatus, error) {
	var st SessionStatus
	return st, c.call(ctx, http.MethodPut, sessionPath(id, ""), spec, &st)
}

// Start makes the session's container and private daemon run.
func (c SessionClient) Start(ctx context.Context, id string) (SessionStatus, error) {
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

// send is call with a raw body, for tars; it answers with the response for
// the caller to read, or a classified error.
func (c SessionClient) send(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", execution.ErrWorkerUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return refusal(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("%w: reading its answer: %w", execution.ErrWorkerUnavailable, err)
	}
	return nil
}

// HasBlob: the worker has the blob.
func (c SessionClient) HasBlob(ctx context.Context, sum string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimRight(c.URL, "/")+"/v1/blobs/"+sum, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %w", execution.ErrWorkerUnavailable, err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("%w: %s", execution.ErrWorkerUnavailable, resp.Status)
}

// PutBlob sends a blob: a tar with one entry named "content", named by its
// SHA-256.
func (c SessionClient) PutBlob(ctx context.Context, sum string, tar io.Reader) error {
	return c.send(ctx, http.MethodPut, "/v1/blobs/"+sum, tar, nil)
}

// PutFiles replaces the session's files with the tar.
func (c SessionClient) PutFiles(ctx context.Context, id string, tar io.Reader) (SessionStatus, error) {
	var st SessionStatus
	return st, c.send(ctx, http.MethodPut, sessionPath(id, "/files"), tar, &st)
}

// Deliver types a message into the chat. An error means it may or may not
// have arrived.
func (c SessionClient) Deliver(ctx context.Context, id, text string) error {
	var out struct{}
	return c.call(ctx, http.MethodPost, sessionPath(id, "/deliver"), DeliverRequest{Text: text}, &out)
}

// Interrupt stops the chat's current turn.
func (c SessionClient) Interrupt(ctx context.Context, id string) error {
	var out struct{}
	return c.call(ctx, http.MethodPost, sessionPath(id, "/interrupt"), nil, &out)
}

// Records lists the chat's record files.
func (c SessionClient) Records(ctx context.Context, id string) ([]RecordFile, error) {
	var out []RecordFile
	return out, c.call(ctx, http.MethodGet, sessionPath(id, "/records"), nil, &out)
}

// Record reads a record file from an offset.
func (c SessionClient) Record(ctx context.Context, id, name string, from int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.URL, "/")+sessionPath(id, "/records/"+url.PathEscape(name))+"?from="+strconv.FormatInt(from, 10), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", execution.ErrWorkerUnavailable, err)
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

// Terminal opens the chat's terminal on the worker.
func (c SessionClient) Terminal(ctx context.Context, id string, size console.Size) (console.Terminal, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s%s?cols=%d&rows=%d", strings.TrimRight(c.URL, "/"), sessionPath(id, "/terminal"), size.Cols, size.Rows), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", terminalProtocol)
	// A terminal lasts as long as it is open: a client's overall timeout
	// would cut it, and would leave its connection unwritable.
	hc := *c.client()
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", execution.ErrWorkerUnavailable, err)
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

// PutSource sends the bundle the chat's work tree is made from.
func (c SessionClient) PutSource(ctx context.Context, id, branch, commit string, bundle io.Reader) (SessionStatus, error) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+sessionPath(id, "/save?"+q.Encode()), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", execution.ErrWorkerUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return "", nil
	}
	if resp.StatusCode >= 300 {
		return "", refusal(resp)
	}
	head := resp.Header.Get("Forge-Head")
	if resp.StatusCode != http.StatusOK || !commitPattern.MatchString(head) {
		return "", fmt.Errorf("%w: the chat's save came back as %d naming commit %q", execution.ErrWorkerUnavailable, resp.StatusCode, head)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, MaxBundleBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("%w: receiving the chat's save: %w", execution.ErrWorkerUnavailable, err)
	}
	if n > MaxBundleBytes {
		return "", fmt.Errorf("%w: the chat's save is larger than %d bytes", gitexec.ErrBundleRefused, MaxBundleBytes)
	}
	return head, nil
}
