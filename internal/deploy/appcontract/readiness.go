package appcontract

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ready is the readiness rule every deployment target applies to one
// answer at an app's health path: the app answers below 400, or exactly
// the status its probe declares. No answer (status 0) is never ready. A
// redirect is an answer, below 400: it is not followed, so the probe
// never takes a header or a secret to another address.
func Ready(status, expect int) bool {
	if status <= 0 {
		return false
	}
	if expect != 0 {
		return status == expect
	}
	return status < 400
}

// ReadinessRequest is one question asked of an app's health path.
type ReadinessRequest struct {
	URL string
	// Host is the request's Host when it is not the URL's (a front door
	// asked at the app's own port).
	Host string
	// Headers are sent as given: the front door's signals, a gate's secret.
	Headers map[string]string
	// Expect is the declared status, 0 for none (Ready).
	Expect int
	// Release, when not "", asks whether the answer serves this release
	// (ServesRelease) instead of judging its status by Expect.
	Release string
	// Timeout bounds the request; 0 leaves it to ctx.
	Timeout time.Duration
}

// ReadinessAnswer is what one sample saw: the status (0 for no answer)
// and whether the app is ready by Ready (and the body, when asked).
type ReadinessAnswer struct {
	Status int
	Ready  bool
}

// SampleReadiness asks r once through transport (nil is Go's default):
// a GET that follows no redirect and reads at most the body it compares.
// A request that got no answer is an error with Status 0.
func SampleReadiness(ctx context.Context, transport http.RoundTripper, r ReadinessRequest) (ReadinessAnswer, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return ReadinessAnswer{}, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	if r.Host != "" {
		req.Host = r.Host
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ReadinessAnswer{}, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // unchecked-error: draining lets the connection be reused; the answer is already read.
		_ = resp.Body.Close()                                         // unchecked-error: the status is the answer; a close failure changes nothing.
	}()
	a := ReadinessAnswer{Status: resp.StatusCode, Ready: Ready(resp.StatusCode, r.Expect)}
	if r.Release != "" {
		// A body that could not be read whole is no answer: a prefix that
		// happens to match the identity is not the marker.
		body, err := io.ReadAll(io.LimitReader(resp.Body, MaxReleaseAnswer))
		if err != nil {
			return ReadinessAnswer{Status: resp.StatusCode}, errors.Join(errors.New("reading the release marker"), err)
		}
		a.Ready = ServesRelease(resp.StatusCode, body, r.Release)
	}
	return a, nil
}

// MaxReleaseAnswer is how much of a release marker's answer is read.
const MaxReleaseAnswer = 4 << 10

// ServesRelease is the one judgement of whether an answer at ReleasePath
// says the site serves release: 200, and the body, without surrounding
// white space, is the identity (ReleaseIdentity). Every target and every
// witness of a static site's release judges its answer by it.
func ServesRelease(status int, body []byte, release string) bool {
	return status == http.StatusOK && release != "" && strings.TrimSpace(string(body)) == release
}
