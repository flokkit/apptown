package portainer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Diagnostic is transport to one verified Docker proxy. Allowed is the owner's
// current authority check; missing authority never admits a diagnostic effect.
type Diagnostic struct {
	Command containerresult.Docker
	Allowed func(context.Context) error
}

// HTTP/1.0 avoids chunked transfer for the bounded release marker's body.
// Requests never follow redirects and never name a destination other than the
// inspected container's loopback namespace. The script only fetches: it
// prints the status, and with PROBE_BODY set the first 4 KiB of the body
// on the lines after it. Go judges both (appcontract.Ready, ServesRelease).
const readinessScript = `ulimit -f 128
{
 printf 'GET %s HTTP/1.0\r\nHost: %s\r\n' "$PROBE_PATH" "$PROBE_HOST"
 if [ -n "$PROBE_FORWARDED_HOST" ]; then printf 'X-Forwarded-Host: %s\r\n' "$PROBE_FORWARDED_HOST"; fi
 if [ -n "$PROBE_FORWARDED_PROTO" ]; then printf 'X-Forwarded-Proto: %s\r\n' "$PROBE_FORWARDED_PROTO"; fi
 printf 'Connection: close\r\n\r\n'
} | nc -w "$PROBE_TIMEOUT" 127.0.0.1 "$PROBE_PORT" > /tmp/reply || exit 3
status=$(awk 'NR==1 {gsub("\r", ""); if ($1 ~ /^HTTP\/1\.[01]$/ && $2 ~ /^[1-5][0-9][0-9]$/) print $2; exit}' /tmp/reply)
[ -n "$status" ] || exit 4
printf '%s\n' "$status"
if [ -n "$PROBE_BODY" ]; then
 offset=$(awk '{n+=length($0)+1; if ($0=="\r") {print n;exit}}' /tmp/reply)
 [ -n "$offset" ] || exit 5
 dd if=/tmp/reply of=/tmp/body bs=1 skip="$offset" 2>/dev/null || exit 6
 head -c 4096 /tmp/body || exit 7
fi`

func (d Diagnostic) sample(ctx context.Context, container string, p Probe) error {
	if d.Command == nil || d.Allowed == nil {
		return errors.New("Portainer readiness diagnostic has no admitted transport authority")
	}
	if container == "" || strings.Trim(container, "0123456789abcdef") != "" || len(container) != 64 {
		return errors.New("Portainer readiness requires an inspected full container ID")
	}
	if !immutableImage(p.ToolImage) || p.Port < 1 || p.Port > 65535 || !strings.HasPrefix(p.Path, "/") || strings.HasPrefix(p.Path, "//") || strings.ContainsAny(p.Path, " \r\n") {
		return errors.New("Portainer readiness probe is invalid")
	}
	for key, value := range p.Headers {
		if (key != "Host" && key != "X-Forwarded-Host" && key != "X-Forwarded-Proto") || strings.ContainsAny(value, "\r\n") {
			return errors.New("Portainer readiness headers are invalid")
		}
	}
	if p.Policy != nil {
		if err := validatePolicy(p.Policy); err != nil {
			return err
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Policy.Timeout)
		defer cancel()
	}
	if err := d.Allowed(ctx); err != nil {
		return err
	}
	if err := d.http(ctx, container, p, p.Path, p.Expect, ""); err != nil {
		return err
	}
	if p.StaticRelease != "" {
		return d.http(ctx, container, p, appcontract.ReleasePath, 0, p.StaticRelease)
	}
	return nil
}

// http asks path once: judged by its status (appcontract.Ready with
// expect), or with release set by whether it serves that release
// (appcontract.ServesRelease).
func (d Diagnostic) http(ctx context.Context, container string, p Probe, path string, expect int, release string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	timeout := 8
	if p.Policy != nil {
		timeout = int(p.Policy.Timeout / time.Second)
	}
	host := p.Headers["Host"]
	if host == "" {
		host = "127.0.0.1:" + strconv.Itoa(p.Port)
	}
	wantBody := ""
	if release != "" {
		wantBody = "1"
	}
	env := []string{"PROBE_TIMEOUT=" + strconv.Itoa(timeout), "PROBE_PATH=" + path, "PROBE_PORT=" + strconv.Itoa(p.Port), "PROBE_HOST=" + host, "PROBE_FORWARDED_HOST=" + p.Headers["X-Forwarded-Host"], "PROBE_FORWARDED_PROTO=" + p.Headers["X-Forwarded-Proto"], "PROBE_BODY=" + wantBody}
	command := func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, errors.New("empty diagnostic command")
		}
		switch args[0] {
		case "create", "start", "rm":
			if err := d.Allowed(ctx); err != nil {
				return nil, err
			}
		}
		return d.Command(ctx, env, args...)
	}
	out, err := containerresult.RunOwned(ctx, command, containerresult.Spec{Image: p.ToolImage, Options: []string{"--network", "container:" + container, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "32", "--memory", "32m", "--cpus", "0.1"}, Env: env, Script: readinessScript, Max: 8 + appcontract.MaxReleaseAnswer})
	if err != nil {
		return fmt.Errorf("Portainer readiness diagnostic: %w", err)
	}
	line, body, _ := strings.Cut(string(out), "\n")
	status, err := strconv.Atoi(line)
	if err != nil || status < 100 || status > 599 {
		return errors.New("Portainer readiness diagnostic returned an invalid verdict")
	}
	// The shell only fetches; the answer is judged by the rules every
	// target applies.
	if release != "" {
		if !appcontract.ServesRelease(status, []byte(body), release) {
			return &predicateFailure{reason: fmt.Sprintf("Portainer readiness predicate failed (HTTP %d, not serving %s)", status, release)}
		}
		return nil
	}
	if !appcontract.Ready(status, expect) {
		return &predicateFailure{reason: fmt.Sprintf("Portainer readiness predicate failed (HTTP %d)", status)}
	}
	return nil
}

type predicateFailure struct {
	reason string
	final  bool
}

func (e *predicateFailure) Error() string { return e.reason }

func (d Diagnostic) preflight(ctx context.Context, image string) error {
	if d.Command == nil || d.Allowed == nil || !immutableImage(image) {
		return errors.New("Portainer diagnostic preflight is not admitted")
	}
	if err := d.Allowed(ctx); err != nil {
		return err
	}
	command := func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		if len(args) > 0 {
			switch args[0] {
			case "create", "start", "rm":
				if err := d.Allowed(ctx); err != nil {
					return nil, err
				}
			}
		}
		return d.Command(ctx, env, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := containerresult.RunOwned(ctx, command, containerresult.Spec{Image: image, Options: []string{"--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges"}, Script: "printf apptown-readiness-v1", Max: 32})
	if err != nil {
		return fmt.Errorf("Portainer diagnostic preflight: %w", err)
	}
	if string(out) != "apptown-readiness-v1" {
		return errors.New("Portainer diagnostic preflight returned no protocol witness")
	}
	return nil
}
