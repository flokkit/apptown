package docker

import (
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids/idstest"
)

type reaperCommandFunc func(context.Context, string, Command) ([]byte, error)

func (f reaperCommandFunc) Run(ctx context.Context, host string, req Command) ([]byte, error) {
	return f(ctx, host, req)
}

// A preview's removal takes its whole stack, chosen by exact owner label:
// the app with its volumes and data volume, its services and an init
// container left standing with theirs, and the network.
func TestRemovingAPreviewTakesItsWholeStackByOwnerLabel(t *testing.T) {
	t.Parallel()
	var calls []string
	var reply func([]string) (string, error, bool)
	scope := Scope{Client: reaperCommandFunc(func(_ context.Context, _ string, req Command) ([]byte, error) {
		calls = append(calls, strings.Join(req.Args, " "))
		out, err, _ := reply(req.Args)
		return []byte(out), err
	})}
	reply = func(args []string) (string, error, bool) {
		call := strings.Join(args, " ")
		if strings.HasPrefix(call, "ps -aq --filter label=forge.class=service --filter label=forge.owner=pvsnap-change-00000000-0007-7000-8000-000000000000") {
			return "svc-a\nsvc-b\n", nil, true
		}
		if strings.HasPrefix(call, "ps -aq --filter label=forge.class=step --filter label=forge.owner=pvsnap-change-00000000-0007-7000-8000-000000000000") {
			return "init-a\n", nil, true
		}
		return "", nil, false
	}
	if err := RemovePreview(context.Background(), scope, PreviewChangeName(idstest.N(7)), ChangeStackID(idstest.N(7))); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"rm -f -v forge-preview-change-00000000-0007-7000-8000-000000000000",
		"volume rm forge-data-forge-preview-change-00000000-0007-7000-8000-000000000000",
		"ps -aq --filter label=forge.class=service --filter label=forge.owner=pvsnap-change-00000000-0007-7000-8000-000000000000",
		"rm -f -v svc-a",
		"volume rm forge-data-svc-a",
		"rm -f -v svc-b",
		"volume rm forge-data-svc-b",
		"ps -aq --filter label=forge.class=step --filter label=forge.owner=pvsnap-change-00000000-0007-7000-8000-000000000000",
		"rm -f -v init-a",
		"volume rm forge-data-init-a",
		"network rm forge-net-pvsnap-change-00000000-0007-7000-8000-000000000000",
	}
	if got := calls; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("RemovePreview issued:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range calls {
		if strings.Contains(c, "name=") || strings.Contains(c, "sh -c") {
			t.Fatalf("reaping by name substring or shell pipeline: %s", c)
		}
	}
}
func TestDockerRemoveLogsOnlyRealFailures(t *testing.T) {
	var calls []string
	var reply func([]string) (string, error, bool)
	scope := Scope{Client: reaperCommandFunc(func(_ context.Context, _ string, req Command) ([]byte, error) {
		calls = append(calls, strings.Join(req.Args, " "))
		out, err, _ := reply(req.Args)
		return []byte(out), err
	})}
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	reply = func(args []string) (string, error, bool) {
		switch strings.Join(args, " ") {
		case "rm -f gone":
			return "Error response from daemon: No such container: gone\n", errors.New("exit status 1"), true
		case "rm -f stuck":
			return "Error response from daemon: cannot remove: permission denied\n", errors.New("exit status 1"), true
		case "ps -aq --filter label=forge.class=service --filter label=forge.owner=pv1":
			return "Cannot connect to the Docker daemon\n", errors.New("exit status 1"), true
		}
		return "", nil, false
	}
	scope.Remove(context.Background(), "gone")
	if buf.Len() != 0 {
		t.Fatalf("an already-gone container is not a failure, logged: %s", buf.String())
	}
	scope.Remove(context.Background(), "stuck")
	if !strings.Contains(buf.String(), "permission denied") {
		t.Fatalf("a real rm failure must be logged, got: %s", buf.String())
	}
	buf.Reset()
	scope.ReapStack(context.Background(), "pv1")
	if !strings.Contains(buf.String(), "Cannot connect") {
		t.Fatalf("a listing that fails must be logged, got: %s", buf.String())
	}
}
