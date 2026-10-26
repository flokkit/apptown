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

// HTTP/1.0 avoids chunked transfer for the bounded exact static identity body.
// Requests never follow redirects and never name a destination other than the
// inspected container's loopback namespace. Only status and comparison escape.
const readinessScript = `ulimit -f 128
{
 printf 'GET %s HTTP/1.0\r\nHost: %s\r\n' "$PROBE_PATH" "$PROBE_HOST"
 if [ -n "$PROBE_FORWARDED_HOST" ]; then printf 'X-Forwarded-Host: %s\r\n' "$PROBE_FORWARDED_HOST"; fi
 if [ -n "$PROBE_FORWARDED_PROTO" ]; then printf 'X-Forwarded-Proto: %s\r\n' "$PROBE_FORWARDED_PROTO"; fi
 printf 'Connection: close\r\n\r\n'
} | nc -w 8 127.0.0.1 "$PROBE_PORT" > /tmp/reply || exit 3
status=$(awk 'NR==1 {gsub("\r", ""); if ($1 ~ /^HTTP\/1\.[01]$/ && $2 ~ /^[1-5][0-9][0-9]$/) print $2; exit}' /tmp/reply)
[ -n "$status" ] || exit 4
matched=1
if [ -n "$PROBE_EXPECT_BODY" ]; then
 offset=$(awk '{n+=length($0)+1; if ($0=="\r") {print n;exit}}' /tmp/reply)
 [ -n "$offset" ] || exit 5
 dd if=/tmp/reply of=/tmp/body bs=1 skip="$offset" 2>/dev/null || exit 6
 printf '%s' "$PROBE_EXPECT_BODY" > /tmp/expected
 cmp -s /tmp/body /tmp/expected || matched=0
fi
printf '%s %s\n' "$status" "$matched"`

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
	if err := d.Allowed(ctx); err != nil {
		return err
	}
	if err := d.http(ctx, container, p, p.Path, p.Expect, ""); err != nil {
		return err
	}
	if p.StaticRelease != "" {
		return d.http(ctx, container, p, appcontract.ReleasePath, 200, p.StaticRelease)
	}
	return nil
}

func (d Diagnostic) http(ctx context.Context, container string, p Probe, path string, expect int, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	host := p.Headers["Host"]
	if host == "" {
		host = "127.0.0.1:" + strconv.Itoa(p.Port)
	}
	env := []string{"PROBE_PATH=" + path, "PROBE_PORT=" + strconv.Itoa(p.Port), "PROBE_HOST=" + host, "PROBE_FORWARDED_HOST=" + p.Headers["X-Forwarded-Host"], "PROBE_FORWARDED_PROTO=" + p.Headers["X-Forwarded-Proto"], "PROBE_EXPECT_BODY=" + body}
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
	out, err := containerresult.RunOwned(ctx, command, containerresult.Spec{Image: p.ToolImage, Options: []string{"--network", "container:" + container, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "32", "--memory", "32m", "--cpus", "0.1"}, Env: env, Script: readinessScript, Max: 32})
	if err != nil {
		return fmt.Errorf("Portainer readiness diagnostic: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return errors.New("Portainer readiness diagnostic returned no bounded verdict")
	}
	status, err := strconv.Atoi(fields[0])
	if err != nil || status < 100 || status > 599 || (fields[1] != "0" && fields[1] != "1") {
		return errors.New("Portainer readiness diagnostic returned an invalid verdict")
	}
	if (expect != 0 && status != expect) || fields[1] != "1" {
		return &predicateFailure{reason: fmt.Sprintf("Portainer readiness predicate failed (HTTP %d, identity match %s)", status, fields[1])}
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
