package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestDockerMaintainedReadinessUsesCapturedRuntimeAndCurrentPermit(t *testing.T) {
	for _, mode := range []string{"healthy", "wrong-status", "replacement", "restart", "wrong-image", "wrong-generation", "missing-start", "wrong-port", "revoked", "unreadable-contract"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/ready" || r.Host != "owned.example" || r.Header.Get("X-Forwarded-Proto") != "https" {
					t.Errorf("wrong admitted request %s %s %s", r.URL, r.Host, r.Header.Get("X-Forwarded-Proto"))
				}
				if mode == "wrong-status" {
					w.WriteHeader(503)
				} else {
					w.WriteHeader(204)
				}
			}))
			defer server.Close()
			_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
			port, _ := strconv.Atoi(portText)
			p := deploy.Placement{ID: idstest.N(1), Label: "owner", Name: "main", Generation: 7, HostPort: port, URL: "https://owned.example"}
			names, err := NamesOf(p.ID, p.Name)
			if err != nil {
				t.Fatal(err)
			}
			p.Container = names.App()
			identity := deploy.DockerIdentity{Deployment: p.Label, Generation: p.Generation, Artifact: "retained-artifact"}
			inspections := 0
			dk := Scope{Client: &fakeClient{reply: func(command Command) ([]byte, error) {
				if command.Args[0] == "port" {
					if mode == "wrong-port" {
						return []byte("127.0.0.1:1"), nil
					}
					return []byte("127.0.0.1:" + portText), nil
				}
				if command.Args[0] != "inspect" {
					return nil, fmt.Errorf("unexpected command %v", command.Args)
				}
				inspections++
				rt := Runtime{ID: "container-a", Image: "sha256:selected", Running: true, Labels: identity.Labels(), State: &ContainerState{StartedAt: time.Unix(1000, 0)}}
				switch mode {
				case "replacement":
					if inspections > 1 {
						rt.ID = "container-b"
					}
				case "restart":
					if inspections > 1 {
						rt.State.StartedAt = time.Unix(1001, 0)
					}
				case "wrong-image":
					rt.Image = "sha256:other"
				case "wrong-generation":
					rt.Labels["apptown.generation"] = "8"
				case "missing-start":
					rt.State.StartedAt = time.Time{}
				}
				return json.Marshal(rt)
			}}}
			now := time.Unix(2000, 0).UTC()
			driver := Driver{Allowed: AuthorisedWhenRequested, Now: func() time.Time { return now }, Scope: func(context.Context) (Scope, error) { return dk, nil }}
			in := ReadinessSample{Placement: p, Artifact: "retained-artifact", Image: "sha256:selected", Part: &appcontract.Part{Name: "web", Port: 8080, Public: true, Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready", Expect: 204}}}}
			if mode == "unreadable-contract" {
				in.Part = nil
			}
			permits := 0
			out, err := driver.Sample(t.Context(), in, func(context.Context) error {
				permits++
				if mode == "revoked" && permits >= 3 {
					return errors.New("revoked")
				}
				return nil
			})
			switch mode {
			case "healthy":
				if err != nil || out.Health != "healthy" || out.RuntimeToken != "container-a/1970-01-01T00:16:40Z" || out.HealthObservedAt != now || out.ArtifactHash != in.Artifact {
					t.Fatalf("exact sample %+v %v", out, err)
				}
			case "wrong-status":
				if err != nil || out.Health != "unhealthy" || out.HealthObservedAt != now {
					t.Fatalf("wrong status became unknown/success: %+v %v", out, err)
				}
			default:
				if err == nil || out.Health == "healthy" || !out.HealthObservedAt.IsZero() {
					t.Fatalf("%s accepted: %+v %v", mode, out, err)
				}
			}
			if strings.HasPrefix(mode, "wrong-") && mode != "wrong-status" && requests != 0 {
				t.Fatal("probe reached mismatched runtime")
			}
		})
	}
}
