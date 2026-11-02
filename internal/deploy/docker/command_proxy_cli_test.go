package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A real CLI's stats fan-out must finish for more containers than the session
// budget, and return each name rather than successful placeholder rows.
func TestCLIStatsQueuesConnectionsWithoutLosingContainers(t *testing.T) {
	t.Parallel()
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Skipf("docker binary is unavailable: %v", err)
	}
	const containers = 20
	// Docker 28 keys collectors by the first twelve ID characters. Real
	// daemon IDs differ there too; zero-padding only the suffix collapses them.
	containerID := func(i int) string { return fmt.Sprintf("%012d%052d", i, i) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.45")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			fmt.Fprint(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			var rows []map[string]any
			for i := range containers {
				rows = append(rows, map[string]any{"Id": containerID(i), "Names": []string{fmt.Sprintf("/tenant-%d", i)}, "State": "running"})
			}
			json.NewEncoder(w).Encode(rows)
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case strings.HasSuffix(r.URL.Path, "/stats"):
			parts := strings.Split(r.URL.Path, "/")
			id, err := strconv.Atoi(parts[len(parts)-2][:12])
			if err != nil || id < 0 || id >= containers {
				t.Errorf("unexpected stats ID: %s", r.URL)
				http.NotFound(w, r)
				return
			}
			for i := range 2 {
				// The daemon includes a leading slash in stats names, just as
				// in container listings. Docker 28 strips that first byte.
				json.NewEncoder(w).Encode(map[string]any{"name": fmt.Sprintf("/tenant-%d", id), "id": containerID(id), "read": time.Now().Add(time.Duration(i) * time.Second), "memory_stats": map[string]any{"usage": 1048576, "limit": 1073741824}, "cpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": 1}}})
				w.(http.Flusher).Flush()
			}
		default:
			t.Errorf("unexpected API request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	slots := make(chan struct{}, 10)
	var active, peak atomic.Int32
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	dk := Scope{Client: CLI{Binary: binary}, Host: "ssh://unused"}
	out, err := dk.Command(ctx, Command{Args: []string{"stats", "--no-stream", "--format", "{{.Name}}\t{{.MemUsage}}"}, Dial: func(ctx context.Context) (net.Conn, error) {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		n := active.Add(1)
		for p := peak.Load(); n > p; p = peak.Load() {
			if peak.CompareAndSwap(p, n) {
				break
			}
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			active.Add(-1)
			<-slots
			return nil, err
		}
		return &countedConn{Conn: conn, release: func() { active.Add(-1); <-slots }}, nil
	}})
	if err != nil {
		t.Fatalf("stats: %v: %s", err, out)
	}
	used, err := parseMemoryUsage(out)
	if err != nil || len(used) != containers {
		t.Fatalf("read %d/%d containers: %v: %s", len(used), containers, err, out)
	}
	for i := range containers {
		name := fmt.Sprintf("tenant-%d", i)
		if got, ok := used[name]; !ok || got != 1048576 {
			t.Errorf("memory for %s: got %d, present %t; want 1048576", name, got, ok)
		}
	}
	if peak.Load() > 10 || active.Load() != 0 {
		t.Fatalf("sessions: peak %d, remaining %d", peak.Load(), active.Load())
	}
	t.Logf("one stats call: %d containers, peak %d sessions, %d remaining", len(used), peak.Load(), active.Load())
}

type countedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *countedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
