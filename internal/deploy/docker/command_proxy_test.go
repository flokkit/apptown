package docker

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// HTTP response completion must close the upstream even when the CLI keeps
// its local connection alive. Otherwise idle sessions starve later stats.
func TestCommandProxyReleasesEachResponseAndStreamsUpgrades(t *testing.T) {
	t.Parallel()
	var opened atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/attach" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			rw.Flush()
			buf, err := io.ReadAll(rw)
			if err != nil {
				return
			}
			rw.Write(buf)
			rw.Flush()
			return
		}
		io.WriteString(w, "reading")
	}))
	defer upstream.Close()
	endpoint, close, err := commandProxy(t.Context(), func(ctx context.Context) (net.Conn, error) {
		opened.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(upstream.URL, "http://"))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	socket := strings.TrimPrefix(endpoint, "unix://")
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	for range 24 {
		r, err := client.Get("http://docker/stats")
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil || string(b) != "reading" {
			t.Fatalf("reading %q: %v", b, err)
		}
	}
	if opened.Load() != 24 {
		t.Fatalf("upstream sessions retained: opened %d", opened.Load())
	}
	req, _ := http.NewRequest("GET", "http://docker/attach", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	stream, ok := r.Body.(io.ReadWriteCloser)
	if !ok || r.StatusCode != 101 {
		t.Fatalf("attach did not upgrade: %v", r.Status)
	}
	if _, err = stream.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err = stream.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err = io.ReadFull(stream, b); err != nil || string(b) != "ping" {
		t.Fatalf("attach: %q %v", b, err)
	}
}
