package docker

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
)

// credentialNeed is a catalog whose Postgres was given a generated
// password, as a deployment's or a preview's is.
type credentialNeed struct{ password string }

func (n credentialNeed) Fulfil(_ context.Context, needs []string, _ map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	if !slices.Contains(needs, "postgres") {
		return nil, nil, nil
	}
	return []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine", Health: "pg_isready", Env: map[string]string{"POSTGRES_PASSWORD": n.password}, Credentials: []string{n.password}}},
		map[string]string{"DATABASE_URL": "postgres://app:" + n.password + "@postgres:5432/app"}, nil
}
func (credentialNeed) Data(context.Context, string) (string, error) { return "", nil }

// leakedRuns is how much of password survives in text: the longest run of
// it, at least 8 characters, found there.
func leakedRun(text, password string) string {
	for n := len(password); n >= 8; n-- {
		for i := 0; i+n <= len(password); i++ {
			if strings.Contains(text, password[i:i+n]) {
				return password[i : i+n]
			}
		}
	}
	return ""
}

// padTo is output that puts what follows it at offset at, so a value
// printed there crosses a cut made at at+few.
func padTo(at int) string { return strings.Repeat("x", at) }

// What a boot says of a failure never holds a credential AppTown made for
// its needs, wherever the app or a migration printed it: a failed init
// step's output and the app's last words on a probe failure (each with the
// value whole, and crossing the cut AppTown makes there) are redacted
// before they are cut, and the error and log tail carry [secret]. These
// are what preview and landing evidence and a worker's answer keep.
func TestABootsFailureNeverHoldsTheNeedsGeneratedCredentials(t *testing.T) {
	t.Parallel()
	const password = "6f1c0a9e5b7d24c38e0f19a6d5b2c7e48a3f60d1b9c2e7a54f0d8b3c6e1a9f27" // gitleaks:allow (a made-up test value)
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	port := strconv.Itoa(closed.Addr().(*net.TCPAddr).Port)
	closed.Close() // nothing answers there: the probe fails
	url := "postgres://app:" + password + "@postgres:5432/app"
	for _, tc := range []struct {
		name, init, logs, phase string
	}{
		{name: "init prints its URL", init: "migration failed: could not connect to " + url + "\n", phase: PhaseParts},
		{name: "init's URL crosses the cut", init: padTo(400-len("could not connect to postgres://app:")-20) + "could not connect to " + url + "\n", phase: PhaseParts},
		{name: "the app logs its password", logs: "booting with PGPASSWORD=" + password + "\n", phase: PhaseProbe},
		{name: "the app's password crosses the cut", logs: padTo(2000-len("PGPASSWORD=")-20) + "PGPASSWORD=" + password + "\n", phase: PhaseProbe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				line := strings.Join(req.Args, " ")
				switch {
				case tc.init != "" && strings.Contains(line, "--name "+Current.Init("pvcreds", "migrate")):
					return []byte(tc.init), errors.New("exit status 1")
				case strings.Contains(line, "State.Health"):
					return []byte("healthy\n"), nil
				case len(req.Args) > 0 && req.Args[0] == "port":
					return []byte("127.0.0.1:" + port + "\n"), nil
				case len(req.Args) > 0 && req.Args[0] == "logs" && diskspacetest.Step(req.Args) == "":
					return []byte(tc.logs), nil
				case len(req.Args) > 0 && req.Args[0] == "image":
					return []byte("\n"), nil
				}
				return nil, nil
			}}
			c, err := appcontract.Parse([]byte("app:\n  web:\n    image: app:1\n    ports: [\"8080\"]\n    public: true\ninit:\n  - name: migrate\n    image: app:1\n    command: [\"migrate\"]\nneeds:\n  postgres: {}\n"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewHost(Dependencies{Secrets: reviewNoSecrets{}, Needs: credentialNeed{password}, Probe: &Probe{Settle: time.Millisecond}}).Boot(context.Background(),
				BootRequest{Docker: Scope{Client: client}, Name: "apptown-preview-creds", StackID: "pvcreds", Workspace: t.TempDir(), Slug: "app", Deployment: "preview", Probe: 200 * time.Millisecond, Scratch: t.TempDir()}, c)
			be := AsBootError(err)
			if err == nil || be.Phase != tc.phase {
				t.Fatalf("the boot's failure: %v (phase %q)", err, be.Phase)
			}
			said := be.Error() + "\n" + be.LogTail
			if run := leakedRun(said, password); run != "" {
				t.Fatalf("the failure keeps %q of the password:\n%s", run, said)
			}
			if !strings.Contains(said, Redacted[:4]) {
				t.Fatalf("the failure does not say a value was redacted:\n%s", said)
			}
		})
	}
}
