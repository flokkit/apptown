package portainer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
)

func TestDiagnosticUsesExactContainerNamespaceAndBoundedVerdict(t *testing.T) {
	for _, mode := range []string{"exact", "wrong-status", "static-identity", "wrong-static-identity", "invalid-verdict", "external-url", "revoked"} {
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
			d := Diagnostic{allowed: func(context.Context) error {
				if mode == "revoked" {
					return errors.New("standing grant revoked")
				}
				return nil
			}, Command: func(ctx context.Context, env []string, args ...string) ([]byte, error) {
				calls++
				if args[0] == "rm" && !slices.Contains(args, "-f") {
					t.Fatalf("diagnostic removed a resource it does not own: %v", args)
				}
				out, err, ok := daemon.RunEnv(args, env)
				if !ok {
					return nil, fmt.Errorf("unexpected command %v", args)
				}
				return out, err
			}}
			err := d.sample(t.Context(), id, "run-1", "dep-1", p)
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
		})
	}
}

// An idle hour of readiness sampling starts at most one container per Docker
// host: the samples run in the deployment's standing reader. A restart of the
// public container is a new run, so it gets one new reader.
func TestIdleReadinessSamplingStartsOneContainerPerHost(t *testing.T) {
	p := Probe{ToolImage: "registry.example/tool@sha256:" + strings.Repeat("a", 64), Port: 8080, Path: "/", Expect: 200}
	id := strings.Repeat("a", 64)
	daemon := containerresulttest.Daemon{Answers: func(containerresulttest.Command) (containerresulttest.Answer, bool) {
		return containerresulttest.Answer{Answer: []byte("200\n")}, true
	}}
	d := Diagnostic{allowed: func(context.Context) error { return nil }, Command: func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		out, err, ok := daemon.RunEnv(args, env)
		if !ok {
			return nil, fmt.Errorf("unexpected command %v", args)
		}
		return out, err
	}}
	for range 3600 / 5 { // a sample every five seconds for an hour
		if err := d.sample(t.Context(), id, "run-1", "dep-1", p); err != nil {
			t.Fatal(err)
		}
	}
	if daemon.Started != 1 {
		t.Fatalf("an idle hour started %d containers, want 1", daemon.Started)
	}
	if err := d.sample(t.Context(), id, "run-2", "dep-1", p); err != nil {
		t.Fatal(err)
	}
	if daemon.Started != 2 || len(daemon.Readers()) != 1 {
		t.Fatalf("a restart left %d started and readers %v, want 2 and one", daemon.Started, daemon.Readers())
	}
}

// Taking a deployment down removes its standing readiness readers, and only
// that deployment's.
func TestRemoveReadersRemovesTheDeploymentsReaders(t *testing.T) {
	var removed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/endpoints/3/docker/containers/json":
			if want := `{"label":["apptown.class=readiness-dep-1"]}`; r.URL.Query().Get("filters") != want {
				t.Errorf("listed %q, want %q", r.URL.Query().Get("filters"), want)
			}
			fmt.Fprint(w, `[{"Id":"r1"}]`)
		case r.Method == "DELETE" && r.URL.Path == "/api/endpoints/3/docker/containers/r1":
			removed = append(removed, r.URL.Path)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	if err := removeReaders(t.Context(), &Target{PortainerURL: server.URL}, 3, "dep-1"); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed %v, want the one reader", removed)
	}
}
