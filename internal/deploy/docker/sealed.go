package docker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/poll"
)

// A sealed boot is a restore rehearsal's (design/subsystems/deployment.md,
// "Restore rehearsals"): production data running somewhere new, so it has no
// address outside its host. Its containers publish no port and join no
// ingress; the app, its needs, its init steps, the copy's import and the
// forge's probe share one network of its own, which nothing else joins.
// That is not isolation from the host: root, the Docker socket, any process
// on the host and any container on the host's network can reach it. A
// rehearsal host is therefore one that runs nothing whose users may not see
// that data.
//
// The probe asks from inside that network and returns a status code and
// nothing else: the response body never leaves the probe's container, so no
// record the forge keeps can hold the data.

// Sealed is what a sealed boot needs beyond an ordinary one.
type Sealed struct {
	// Fill fills the stack's new, empty needs from a copy, on its own
	// network, after they are healthy and before any init step reads them.
	Fill func(network string) error
	// ProbeImage runs the in-network probe; it needs a shell, nc and awk
	// (busybox has them).
	ProbeImage string
	// Host is the host name the probe's requests carry: the deployment
	// the rehearsal stands in for, as its visitors reach it.
	Host string
}

// probeScript sends one GET and prints the status code and, on a redirect,
// the Location header: the app's answer goes no further than this
// container. The request's parts arrive as environment values, never as
// shell text.
const probeScript = `printf 'GET %s HTTP/1.1\r\nHost: %s\r\nX-Forwarded-Proto: https\r\nX-Forwarded-Host: %s\r\nConnection: close\r\n\r\n' "$PROBE_PATH" "$PROBE_HOST" "$PROBE_HOST" | nc -w 8 "$PROBE_TARGET" "$PROBE_PORT" > /tmp/answer || exit 3
tr -d '\r' < /tmp/answer | awk 'NR==1 {print $2; next} /^$/ {exit} tolower($1)=="location:" {print $2; exit}'`

var statusLine = regexp.MustCompile(`^[1-5][0-9][0-9]$`)

// ErrNoAnswer is a sealed probe that got no HTTP answer.
var ErrNoAnswer = errors.New("no HTTP answer")

// probeRedirects bounds the same-host redirects the sealed page probe follows, as
// the page probe does (check.ProbePage).
const probeRedirects = 5

// Get asks the app container at port, on the sealed stack's network, for
// path, and returns the status it answers. A readiness or data check is
// judged on that answer: a redirect is its status, never where it leads (a
// data path that redirects to a sign-in page has not read the data). The
// front page follows redirects to the same host (Page). Nothing but the
// status is returned.
func (s *Sealed) Get(ctx context.Context, dk Scope, network, container string, port int, path string) (int, error) {
	status, _, err := s.getOnce(ctx, dk, network, container, port, path)
	return status, err
}

// Page asks for the root as a visitor arrives, following same-host
// redirects (to /login, /dashboard) as the page probe does
// (check.ProbePage), and returns the status it ends on. A redirect to
// another host is the answer as it stands and is never followed, so one
// naming production never reaches production; every request goes to the
// rehearsal's container.
func (s *Sealed) Page(ctx context.Context, dk Scope, network, container string, port int) (int, error) {
	path := "/"
	for i := 0; ; i++ {
		status, location, err := s.getOnce(ctx, dk, network, container, port, path)
		if err != nil || status < 300 || status >= 400 || i == probeRedirects || location == "" {
			return status, err
		}
		loc, err := url.Parse(location)
		if err != nil || (loc.Host != "" && !strings.EqualFold(loc.Hostname(), s.Host)) {
			return status, nil
		}
		next := loc.RequestURI()
		if !strings.HasPrefix(next, "/") {
			next = "/" + next
		}
		path = next
	}
}

func (s *Sealed) getOnce(ctx context.Context, dk Scope, network, container string, port int, path string) (int, string, error) {
	if strings.ContainsAny(path, " \r\n") || !strings.HasPrefix(path, "/") {
		return 0, "", fmt.Errorf("a probe path is one absolute path, not %q", path)
	}
	args := []string{"run", "--rm", "--network", network, "--entrypoint", "",
		"-e", "PROBE_PATH", "-e", "PROBE_HOST", "-e", "PROBE_TARGET", "-e", "PROBE_PORT"}
	args = append(args, dk.Labels("step", "sealed-probe")...)
	args = append(args, s.ProbeImage, "sh", "-c", probeScript)
	env := []string{"PROBE_PATH=" + path, "PROBE_HOST=" + s.Host, "PROBE_TARGET=" + container, "PROBE_PORT=" + strconv.Itoa(port)}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := dk.Command(ctx, Command{Args: args, Env: env})
	if err != nil {
		// The probe's own output is not repeated: it can hold the app's.
		return 0, "", ErrNoAnswer
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	code := strings.TrimSpace(lines[0])
	if !statusLine.MatchString(code) {
		return 0, "", ErrNoAnswer
	}
	n, _ := strconv.Atoi(code)
	location := ""
	if len(lines) > 1 {
		location = strings.TrimSpace(lines[1])
	}
	return n, location, nil
}

// awaitReady asks the readiness path until it answers as the probe wants,
// while the container runs, until wait ends. Any status proves something
// serves unless the probe expects one.
func (s *Sealed) awaitReady(ctx context.Context, dk Scope, network, container string, port int, rp RenderedProbe, wait time.Duration) bool {
	path := rp.Path
	if path == "" || rp.TCP || len(rp.Exec) > 0 {
		path = "/"
	}
	ctx = dk.lifetime(ctx)
	ready := false
	poll.Until(ctx, wait, time.Second, func() bool {
		if !dk.Running(ctx, container) {
			return true // stopped: it will not answer
		}
		status, err := s.Get(ctx, dk, network, container, port, path)
		ready = err == nil && (rp.Expect == 0 || status == rp.Expect)
		return ready
	})
	return ready
}
