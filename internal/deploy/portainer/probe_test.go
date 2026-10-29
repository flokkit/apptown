package portainer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
)

func TestDiagnosticUsesExactContainerNamespaceAndBoundedVerdict(t *testing.T) {
	for _, mode := range []string{"exact", "wrong-status", "static-identity", "wrong-static-identity", "invalid-verdict", "external-url", "revoked", "cleanup-failed"} {
		t.Run(mode, func(t *testing.T) {
			_, r := readinessFixture(t)
			p := r.Public
			p.Headers = map[string]string{"Host": "app.example", "X-Forwarded-Host": "app.example", "X-Forwarded-Proto": "https"}
			if mode == "static-identity" || mode == "wrong-static-identity" {
				p.StaticRelease = "release-id"
			}
			if mode == "external-url" {
				p.Path = "https://app.example/"
			}
			id := strings.Repeat("a", 64)
			calls := 0
			daemon := containerresulttest.Daemon{Answers: func(command containerresulttest.Command) (containerresulttest.Answer, bool) {
				args := command.Args
				network := slices.Index(args, "--network")
				if network < 0 || args[network+1] != "container:"+id {
					t.Errorf("probe escaped exact namespace: %v", args)
				}
				if command.Image != p.ToolImage || slices.Contains(args, "-v") || slices.Contains(args, "--privileged") {
					t.Errorf("unsafe diagnostic: %v", args)
				}
				if !slices.Contains(command.Env, "PROBE_HOST=app.example") || !slices.Contains(command.Env, "PROBE_FORWARDED_PROTO=https") {
					t.Errorf("lost front-door predicate: %v", command.Env)
				}
				answer := "204\n"
				if mode == "wrong-status" {
					answer = "302\n"
				}
				if mode == "invalid-verdict" {
					answer = "healthy\n"
				}
				if slices.Contains(command.Env, "PROBE_PATH=/_apptown/release") {
					if !slices.Contains(command.Env, "PROBE_BODY=1") {
						t.Errorf("the release marker was asked without its body: %v", command.Env)
					}
					// The file server's marker, judged by
					// appcontract.ServesRelease: its line ending is not part of it.
					answer = "200\nrelease-id\n"
					if mode == "wrong-static-identity" {
						answer = "200\nother-release\n"
					}
				}
				return containerresulttest.Answer{Answer: []byte(answer)}, true
			}}
			d := Diagnostic{Allowed: func(context.Context) error {
				if mode == "revoked" {
					return errors.New("standing grant revoked")
				}
				return nil
			}, Command: func(ctx context.Context, env []string, args ...string) ([]byte, error) {
				calls++
				if args[0] == "ps" {
					t.Fatal("diagnostic swept sibling resources")
				}
				if mode == "cleanup-failed" && args[0] == "rm" {
					return nil, errors.New("remove unavailable")
				}
				out, err, ok := daemon.RunEnv(args, env)
				if !ok {
					return nil, fmt.Errorf("unexpected command %v", args)
				}
				return out, err
			}}
			err := d.sample(t.Context(), id, p)
			if mode == "exact" || mode == "static-identity" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid readiness became successful")
			}
			if (mode == "external-url" || mode == "revoked") && calls != 0 {
				t.Fatalf("refusal made %d Docker calls", calls)
			}
			if mode == "cleanup-failed" && !strings.Contains(err.Error(), "removing owned result apptown-result-") {
				t.Fatalf("cleanup resource evidence lost: %v", err)
			}
		})
	}
}
