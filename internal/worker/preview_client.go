package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// processSession is the session a Previews without its own uses: this
// process's start, in nanoseconds, on the real clock: nothing gives a
// process's start a clock of its own. An AppTown sets its own (NextSession),
// so its sessions keep growing across its restarts whatever the clock does.
// lastSeq numbers the requests that change previews.
var (
	processSession = strconv.FormatInt(clock.Or(nil)().UnixNano(), 10)
	lastSeq        atomic.Int64
)

// NextSession is the next AppTown session number, kept in file: greater
// than every earlier one, and than AppTown's clock (now; nil is the real
// one), and written durably before it is answered. A counter that cannot be
// read or written is an error, and no session: two starts must never share
// one.
func NextSession(file string, now func() time.Time) (int64, error) {
	var prev int64
	body, err := os.ReadFile(file)
	switch {
	case err == nil:
		if prev, err = strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64); err != nil || prev <= 0 {
			return 0, fmt.Errorf("AppTown's session count in %s cannot be read: %q", file, strings.TrimSpace(string(body)))
		}
	case !errors.Is(err, os.ErrNotExist):
		return 0, err
	}
	next := max(clock.Or(now)().UnixNano(), prev+1)
	if err := writeDurable(filepath.Dir(file), filepath.Base(file), []byte(strconv.FormatInt(next, 10)+"\n")); err != nil {
		return 0, fmt.Errorf("keeping AppTown's session count in %s: %w", file, err)
	}
	return next, nil
}

// stamp marks a request that changes a preview with the session and its
// next number.
func (p Previews) stamp(req *http.Request) {
	session := p.Session
	if session == "" {
		session = processSession
	}
	req.Header.Set(PreviewSessionHeader, session)
	req.Header.Set(PreviewSeqHeader, strconv.FormatInt(lastSeq.Add(1), 10))
}

// Previews is AppTown's side of previews on a worker: boot an incarnation
// there, read it, remove it, and forward visitors to it. Every request that
// changes a preview carries its AppTown session and a number within it, an
// ordered token; the worker refuses one older than the last it acted on for
// that preview, or than the latest claim (removing every preview), so a
// request delayed past a later one never undoes it.
type Previews struct {
	URL    string
	Client *http.Client
	// Session is AppTown session its requests belong to, a positive
	// number (NextSession); "" is this process's start.
	Session string
}

// Boot sends a preview's spec, prepared value and tree, and answers with
// what the worker has running, or the boot's own failure.
func (p Previews) Boot(ctx context.Context, spec PreviewSpec, prepared docker.Prepared, tree string) (PreviewState, error) {
	ctx, cancel := context.WithTimeout(ctx, previewLifetime+checkGrace)
	defer cancel()
	body, content := io.Pipe()
	form := multipart.NewWriter(content)
	go func() {
		content.CloseWithError(func() error {
			for _, part := range []struct {
				name string
				v    any
			}{{"spec", spec}, {"prepared", prepared}} {
				w, err := form.CreateFormField(part.name)
				if err != nil {
					return err
				}
				if err := json.NewEncoder(w).Encode(part.v); err != nil {
					return err
				}
			}
			w, err := form.CreateFormFile("tree", "tree.tar")
			if err != nil {
				return err
			}
			if err := tarTree(w, tree); err != nil {
				return fmt.Errorf("sending the preview's tree: %w", err)
			}
			return form.Close()
		}())
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.URL+"/v1/previews/"+url.PathEscape(spec.Name), body)
	if err != nil {
		return PreviewState{}, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	p.stamp(req)
	return p.state(req)
}

// Admit asks the worker whether its daemon can give each part and need of
// c the resources it declares; it does nothing there. A refusal is the
// worker's sentence.
func (p Previews) Admit(ctx context.Context, c *appcontract.Contract) error {
	ctx, cancel := context.WithTimeout(ctx, checkGrace)
	defer cancel()
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/v1/preview-admission", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("asking the worker whether it can run the preview: %w", err)
	}
	defer resp.Body.Close()
	// A worker of the release before has no admission to ask (404): it
	// boots as it did before admission existed, with nothing checked. One
	// release of skew is supported (compatibility.md).
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return &AdmissionRefused{Reason: strings.TrimSpace(string(msg))}
	}
	return fmt.Errorf("asking the worker whether it can run the preview: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
}

// AdmissionRefused is the worker's refusal of a preview its daemon cannot
// hold: the contract's answer, not the worker's unavailability.
type AdmissionRefused struct{ Reason string }

func (e *AdmissionRefused) Error() string { return "the worker refuses the preview: " + e.Reason }

// State is the preview as the worker has it now.
func (p Previews) State(ctx context.Context, name string) (PreviewState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+"/v1/previews/"+url.PathEscape(name), nil)
	if err != nil {
		return PreviewState{}, err
	}
	return p.state(req)
}

// ErrClaimRequired is the worker refusing a request because it lost the
// order of its previews' requests: AppTown must claim it again.
var ErrClaimRequired = errors.New("the worker lost the order of its previews' requests, and must be claimed again")

func claimRequired(resp *http.Response) bool {
	return resp.StatusCode == http.StatusConflict && resp.Header.Get(PreviewClaimHeader) == "required"
}

func (p Previews) state(req *http.Request) (PreviewState, error) {
	resp, err := p.Client.Do(req)
	if err != nil {
		return PreviewState{}, fmt.Errorf("the worker did not answer: %w", err)
	}
	defer resp.Body.Close()
	if claimRequired(resp) {
		return PreviewState{}, ErrClaimRequired
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return PreviewState{}, fmt.Errorf("the worker refused (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var st PreviewState
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st); err != nil {
		return PreviewState{}, fmt.Errorf("the worker's answer: %w", err)
	}
	return st, nil
}

// List is the name of every preview on the worker.
func (p Previews) List(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+"/v1/previews", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the worker did not answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("the worker refused (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var names []string
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&names); err != nil {
		return nil, fmt.Errorf("the worker's answer: %w", err)
	}
	return names, nil
}

// Remove removes one preview's whole stack on the worker; "" removes every
// preview there.
func (p Previews) Remove(ctx context.Context, name string) error {
	path := "/v1/previews"
	if name != "" {
		path += "/" + url.PathEscape(name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, p.URL+path, nil)
	if err != nil {
		return err
	}
	p.stamp(req)
	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("the worker did not answer: %w", err)
	}
	defer resp.Body.Close()
	if claimRequired(resp) {
		return ErrClaimRequired
	}
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.New(strings.TrimSpace(fmt.Sprintf("removing the preview on the worker (%d): %s", resp.StatusCode, msg)))
	}
	return nil
}

// RemoveImages asks the worker to remove the build images a stopped
// change's preview kept.
func (p Previews) RemoveImages(ctx context.Context, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, p.URL+"/v1/previews/"+url.PathEscape(name)+"/images", nil)
	if err != nil {
		return err
	}
	p.stamp(req)
	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("the worker did not answer: %w", err)
	}
	defer resp.Body.Close()
	if claimRequired(resp) {
		return ErrClaimRequired
	}
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.New(strings.TrimSpace(fmt.Sprintf("removing the preview's images on the worker (%d): %s", resp.StatusCode, msg)))
	}
	return nil
}

// Forward is a proxy of visitors' requests to the named preview on the
// worker: the visitor's Host and request target (path and query, escaped as
// sent) go through unchanged, in headers the worker's router never reads as
// its own path; the forwarding headers are AppTown's own (publicScheme,
// the visitor's address); and upgrades such as WebSockets pass through.
// publicScheme "" is the naked door a probe uses: no forwarding headers at
// all, as a request to the app's own port has none.
func (p Previews) Forward(name, publicScheme string) *httputil.ReverseProxy {
	base, _ := url.Parse(p.URL)
	return &httputil.ReverseProxy{
		Transport: p.Client.Transport,
		Rewrite: func(req *httputil.ProxyRequest) {
			req.Out.URL = &url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/v1/previews/" + name + "/http"}
			req.Out.Host = base.Host
			req.Out.Header.Set(PreviewHostHeader, req.In.Host)
			req.Out.Header.Set(PreviewTargetHeader, req.In.URL.RequestURI())
			// Whatever forwarding headers a visitor sent are dropped; these
			// are AppTown's own.
			StripForwarding(req.Out.Header)
			if publicScheme != "" {
				req.SetXForwarded()
				req.Out.Header.Set("X-Forwarded-Proto", publicScheme)
				req.Out.Header.Set("X-Forwarded-Host", req.In.Host)
			}
		},
	}
}

// StripForwarding removes every forwarding header a request carries: the
// whole X-Forwarded-* family, Forwarded and X-Real-Ip. A proxy that sets its
// own forwarding headers first removes whatever a visitor sent, so an app
// behind it trusts only the proxy's.
func StripForwarding(h http.Header) {
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Forwarded-") {
			delete(h, k)
		}
	}
	h.Del("Forwarded")
	h.Del("X-Real-Ip")
}
