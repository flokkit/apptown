package worker

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// A task's private services run under the worker's service limits (its
// -service-* flags), the defaults when it names none: the same bounds a
// check's services get. Their logs are bounded as a deployment's are.
func TestPrivateServicesRunUnderTheWorkersServiceLimits(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var calls []string
	fake := dockerFunc(func(args []string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, strings.Join(args, " "))
		mu.Unlock()
		if args[0] == "inspect" {
			return []byte("running none\n"), nil
		}
		return nil, nil
	})
	rec := record{Key: "agent-1", Private: &privateSet{Network: "apptown-private-agent-1",
		Spec:     execution.PrivateDocker{Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine"}}},
		Services: []string{"apptown-private-agent-1-postgres"}}}
	for _, c := range []struct {
		limits execution.Limits
		want   string
	}{
		{execution.Limits{}, "--cpus 1 --memory 1073741824 --memory-swap 1073741824 --pids-limit 256"},
		{execution.Limits{CPUs: 2, MemoryBytes: 2 << 30, PIDs: 512}, "--cpus 2 --memory 2147483648 --memory-swap 2147483648 --pids-limit 512"},
	} {
		mu.Lock()
		calls = nil
		mu.Unlock()
		s := &Server{ToolVersions: stock.Versions(), State: t.TempDir(), Docker: fake, ServiceLimits: c.limits}
		if err := s.makePrivate(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		recorded := strings.Join(calls, "\n")
		mu.Unlock()
		if !strings.Contains(recorded, "--network-alias postgres") || !strings.Contains(recorded, c.want) {
			t.Errorf("limits %+v: the service ran as %s", c.limits, recorded)
		}
		if !strings.Contains(recorded, "--log-driver json-file --log-opt max-size=10m --log-opt max-file=3") {
			t.Errorf("the service ran without a bounded log: %s", recorded)
		}
	}
}

// A worker agent step's need that keeps data gets a disposable folder there
// (-v <path>, an anonymous volume the removal's -v takes); one with no data
// path gets none. S3's emulator exits at once without /data.
func TestAPrivateNeedThatKeepsDataGetsADisposableFolderThere(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var calls []string
	fake := dockerFunc(func(args []string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, strings.Join(args, " "))
		mu.Unlock()
		if args[0] == "inspect" {
			return []byte("running none\n"), nil
		}
		return nil, nil
	})
	rec := record{Key: "agent-2", Private: &privateSet{Network: "apptown-private-agent-2",
		Spec: execution.PrivateDocker{Services: []deploy.ServiceSpec{
			{Name: "s3", Image: "versity/versitygw:v1.8.0", Data: "/data", Entrypoint: []string{"/bin/sh", "-c"}, Command: []string{"start"}}, {Name: "cache", Image: "redis:7"}}},
		Services: []string{"apptown-private-agent-2-s3", "apptown-private-agent-2-cache"}}}
	s := &Server{ToolVersions: stock.Versions(), State: t.TempDir(), Docker: fake}
	if err := s.makePrivate(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var s3, cache string
	for _, c := range calls {
		switch {
		case strings.HasPrefix(c, "run ") && strings.Contains(c, "--network-alias s3 "):
			s3 = c
		case strings.HasPrefix(c, "run ") && strings.Contains(c, "--network-alias cache "):
			cache = c
		}
	}
	// It is started by its script: the entrypoint replaced, the rest of it
	// before the command.
	if !strings.HasSuffix(s3, " -v /data --entrypoint /bin/sh versity/versitygw:v1.8.0 -c start") {
		t.Fatalf("s3 started without a folder at /data, or not by its script: %q", s3)
	}
	if cache == "" || strings.Contains(cache, " -v ") {
		t.Fatalf("a service with no data path got a volume: %q", cache)
	}
}
