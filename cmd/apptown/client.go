package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/version"
)

// client is the apptownd API as the CLI speaks it: one base URL, one actor, the
// same credentials on every request, and one rule for when to try again.
type client struct {
	base  string // FORGE_URL
	actor string // FORGE_USER, sent as X-Forge-Actor on every request
	http  *http.Client
	// wait pauses between attempts; getenv reads FORGE_TOKEN and
	// FORGE_SESSION_TOKEN at call time. Both are replaced in tests.
	wait   func(time.Duration)
	getenv func(string) string
}

var api = &client{
	base:   envOr("FORGE_URL", "http://localhost:7777"),
	actor:  envOr("FORGE_USER", envOr("USER", "anonymous")),
	http:   http.DefaultClient,
	wait:   pause,
	getenv: os.Getenv,
}

// pause is every wait the CLI takes: between polls of a follow loop, and
// before a retry. It waits on the process's root; Ctrl-C's default action
// ends the process, and the wait with it.
func pause(d time.Duration) { _ = poll.Wait(root, d) }

// send makes one API request, trying again where that cannot double an
// effect, and returns the response for the caller to read.
func (c *client) send(method, path string, body []byte) (*http.Response, error) {
	attempt := func() (*http.Response, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, c.base+"/api/v1"+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		c.authorize(req)
		if safeToRepeat(method, path) {
			return c.http.Do(req)
		}
		return c.once().Do(req)
	}
	res, err := attempt()
	// A apptownd restart (a deploy) drops in-flight connections with EOF or a
	// refusal for a few seconds. Reads and the read-only verifications
	// (check, preview) are safe to retry; a mutation is sent again only when
	// it never left (the connection was never made). Otherwise its outcome
	// is unknown, so it reports and lets the person look (complaints #47, #57).
	for n := 1; err != nil && n <= 3 && retryableRequest(method, path, err); n++ {
		c.wait(time.Duration(n*4) * time.Second)
		res, err = attempt()
	}
	if err != nil {
		return nil, c.failed(method, path, err)
	}
	return c.afterGateway(res, method, path, attempt)
}

// failed is a request that got no answer. A mutation that may have left
// (anything but a connection never made) may have been acted on: its outcome
// is unknown. Anything else is an ordinary failure.
func (c *client) failed(method, path string, err error) error {
	if !safeToRepeat(method, path) && !neverLeft(err) {
		return &unknownOutcome{Method: method, Path: path, Lost: err}
	}
	return fmt.Errorf("%s %s: %w (is apptownd running at %s?)", method, path, err, c.base)
}

// once is the HTTP client for a mutation: c.http over HTTP/1.1 only. Go's
// HTTP/2 transport sends a request again by itself after some stream
// errors (a peer's RST_STREAM PROTOCOL_ERROR, a GOAWAY), which do not prove
// the peer did nothing; its HTTP/1.1 transport repeats a request only when
// none of it was written. A mutation is then sent at most once below the
// CLI's own rule (send).
func (c *client) once() *http.Client {
	base := c.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	t, ok := base.(*http.Transport)
	if !ok {
		return c.http // a test's own round tripper
	}
	h1 := t.Clone()
	h1.Protocols = new(http.Protocols)
	h1.Protocols.SetHTTP1(true)
	if h1.TLSClientConfig != nil {
		// A configured ALPN list offering h2 would still let the server
		// pick it.
		cfg := h1.TLSClientConfig.Clone()
		// Clone shares NextProtos' array; filter a copy, or the original
		// transport's list is edited in place.
		cfg.NextProtos = slices.DeleteFunc(slices.Clone(cfg.NextProtos), func(p string) bool { return p == "h2" })
		h1.TLSClientConfig = cfg
	}
	return &http.Client{Transport: h1, CheckRedirect: c.http.CheckRedirect, Jar: c.http.Jar, Timeout: c.http.Timeout}
}

// afterGateway handles a 502, 503 or 504: a restart behind the front door,
// or apptownd's own unavailability. A read asks again. A mutation that got
// one is never sent again: a proxy answers so also when apptownd did the
// work and its answer was lost, so the outcome is unknown.
func (c *client) afterGateway(res *http.Response, method, path string, attempt func() (*http.Response, error)) (*http.Response, error) {
	if !gatewayFailure(res.StatusCode) {
		return res, nil
	}
	if !safeToRepeat(method, path) {
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var e struct{ Error string }
		_ = json.Unmarshal(raw, &e)
		return nil, &unknownOutcome{Method: method, Path: path, Status: res.StatusCode, Said: e.Error}
	}
	var err error
	for n := 1; err == nil && gatewayFailure(res.StatusCode) && n <= 4; n++ {
		res.Body.Close()
		fmt.Fprintf(os.Stderr, "… AppTown is restarting (HTTP %d), retrying\n", res.StatusCode)
		c.wait(time.Duration(n*4) * time.Second)
		res, err = attempt()
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w (is apptownd running at %s?)", method, path, err, c.base)
	}
	return res, nil
}

func gatewayFailure(status int) bool { return status == 502 || status == 503 || status == 504 }

// unknownOutcome: a mutation was sent and the answer said nothing about
// whether apptownd acted on it: a gateway's status, or the connection broke
// after the request left (Lost). The CLI exits with exitUnknown.
type unknownOutcome struct {
	Method, Path string
	Status       int
	Said         string // apptownd's own sentence, when it gave one
	Lost         error  // the broken connection, when there was no status
}

func (e *unknownOutcome) Error() string {
	got := fmt.Sprintf("HTTP %d", e.Status)
	if e.Lost != nil {
		got = fmt.Sprintf("the connection broke after the request was sent (%v)", e.Lost)
	}
	msg := fmt.Sprintf("%s %s: %s; the outcome is unknown — check with `apptown show …` before trying again", e.Method, e.Path, got)
	if e.Said != "" {
		msg += " (AppTown said: " + e.Said + ")"
	}
	return msg
}

// exitUnknown is the exit code for a mutation whose outcome is unknown.
const exitUnknown = 4

// authorize sets the same actor and credentials on every verb (complaint
// #69: diff once answered "sign in required" with a valid FORGE_TOKEN).
func (c *client) authorize(req *http.Request) {
	// The protocol this CLI speaks: a forge outside it refuses by name.
	req.Header.Set(version.CLIProtocolHeader, strconv.Itoa(version.CLIProtocol))
	req.Header.Set("X-Forge-Actor", c.actor)
	if tok := c.getenv("FORGE_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if tok := c.getenv("FORGE_SESSION_TOKEN"); tok != "" {
		req.Header.Set("X-Forge-Session-Token", tok)
	}
}

// request sends body as JSON and returns the response body; an HTTP error is
// the server's own sentence when it gave one.
func (c *client) request(method, path string, body any) (json.RawMessage, error) {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	res, err := c.send(method, path, b)
	if err != nil {
		return nil, err
	}
	return c.answer(res, method, path)
}

// answer reads a response: its body, or the server's own sentence when it
// refused.
func (c *client) answer(res *http.Response, method, path string) (json.RawMessage, error) {
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		if !safeToRepeat(method, path) {
			// The forge answered and the answer broke off: what it did is
			// not known from here.
			return nil, &unknownOutcome{Method: method, Path: path, Status: res.StatusCode, Lost: err}
		}
		return nil, err
	}
	if res.StatusCode >= 400 {
		code := res.Header.Get("X-Forge-Deny-Code")
		var e struct{ Error string }
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, &httpError{Status: res.StatusCode, Code: code, msg: e.Error}
		}
		return nil, &httpError{Status: res.StatusCode, Code: code, msg: fmt.Sprintf("%s %s: HTTP %d", method, path, res.StatusCode)}
	}
	return raw, nil
}

// httpError is the forge's answer to a request it refused: the server's own
// sentence, and the status, so a caller can tell an answer that will not
// change (no such run) from a failure worth trying again. Code is the
// refusal's stable code, when the forge gave one: what a caller branches on,
// never the sentence.
type httpError struct {
	Status int
	Code   string
	msg    string
}

// refusalCode is err's code when the forge refused with one, else "".
func refusalCode(err error) string {
	var h *httpError
	if errors.As(err, &h) {
		return h.Code
	}
	return ""
}

func (e *httpError) Error() string { return e.msg }

// final: the forge answered, and asking again will get the same answer.
// A read's 502/503/504 during a restart was retried in send already; a
// timeout or a rate limit may yet change.
func (e *httpError) final() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != 408 && e.Status != 429
}

// upload sends a file as the request body, once: an upload is a mutation,
// so it is sent again, from the start of the file, only when it never left
// (the connection was never made). A 502/503/504 is an unknown outcome.
func (c *client) upload(path string, f *os.File) (json.RawMessage, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	attempt := func() (*http.Response, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		req, err := http.NewRequest("POST", c.base+"/api/v1"+path, io.NopCloser(f))
		if err != nil {
			return nil, err
		}
		req.ContentLength = info.Size()
		c.authorize(req)
		req.Header.Set("Content-Type", "application/x-git-bundle")
		return c.once().Do(req)
	}
	res, err := attempt()
	for n := 1; err != nil && n <= 3 && neverLeft(err); n++ {
		c.wait(time.Duration(n*4) * time.Second)
		res, err = attempt()
	}
	if err != nil {
		return nil, c.failed("POST", path, err)
	}
	if res, err = c.afterGateway(res, "POST", path, attempt); err != nil {
		return nil, err
	}
	return c.answer(res, "POST", path)
}

// download streams a GET's body to w, returning how many bytes came.
func (c *client) download(path string, w io.Writer) (int64, error) {
	res, err := c.send("GET", path, nil)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		_, err := c.answer(res, "GET", path)
		return 0, err
	}
	n, err := io.Copy(w, res.Body)
	if err != nil {
		return n, fmt.Errorf("the download broke off after %d bytes: %w", n, err)
	}
	return n, nil
}

func (c *client) post(path string, body, out any) error {
	raw, err := c.request("POST", path, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *client) get(path string, out any) error {
	raw, err := c.request("GET", path, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// text reads a plain-text answer (a diff); an HTTP error is the body as sent.
func (c *client) text(path string) (string, error) {
	res, err := c.send("GET", path, nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if res.StatusCode >= 400 {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(raw)))
	}
	return string(raw), nil
}

// retryableRequest: a transport failure (not an HTTP status) on a request
// whose repeat cannot double an effect: a read or read-only verification
// whose connection broke, or any request that never left.
func retryableRequest(method, path string, err error) bool {
	if neverLeft(err) {
		return true
	}
	msg := err.Error()
	transient := strings.Contains(msg, "EOF") || strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe")
	return transient && safeToRepeat(method, path)
}

// safeToRepeat: a read, or a read-only verification (check, preview,
// config validation), whose repeat cannot double an effect.
func safeToRepeat(method, path string) bool {
	if method == "GET" {
		return true
	}
	return strings.HasSuffix(path, "/check") || strings.HasSuffix(path, "/preview") || strings.HasSuffix(path, "/config/validate")
}

// neverLeft: the connection was never made, so the request was not sent.
func neverLeft(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}
