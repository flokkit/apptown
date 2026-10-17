package worker_test

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// seen is what the preview's app was sent.
type seen struct {
	Host, Path, Query string
	RequestURI        string
	Header            http.Header
}

// forwardedPreview is an app on this machine's loopback that a fake Docker
// reports as the preview forge-preview-change-7's port, a worker over the
// forge's authenticated connection, and the forge's side forwarding to it.
func forwardedPreview(t *testing.T) (forge *httptest.Server, workerURL string, client *http.Client, got func() seen, dk *fileDocker) {
	t.Helper()
	var mu sync.Mutex
	var last seen
	upgrader := websocket.Upgrader{}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = seen{RequestURI: r.RequestURI, Host: r.Host, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Header: r.Header.Clone()}
		mu.Unlock()
		if websocket.IsWebSocketUpgrade(r) {
			c, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			for {
				kind, msg, err := c.ReadMessage()
				if err != nil {
					return
				}
				_ = c.WriteMessage(kind, append([]byte("echo "), msg...))
			}
		}
		fmt.Fprint(w, "the app")
	}))
	t.Cleanup(app.Close)
	port := app.Listener.Addr().(*net.TCPAddr).Port

	dk, _ = sweepDocker(t)
	dk.reply = func(args []string) ([]byte, error, bool) {
		switch {
		case len(args) == 2 && args[0] == "port" && args[1] == "forge-preview-change-7":
			return []byte(fmt.Sprintf("3000/tcp -> 127.0.0.1:%d\n", port)), nil, true
		case len(args) > 0 && args[0] == "port":
			return []byte("Error: No such container: " + args[len(args)-1] + "\n"), errExit, true
		}
		return nil, nil, true
	}
	ca := workertest.NewCA(t)
	state := t.TempDir()
	// forge-preview-change-9 is a preview this worker holds that is not
	// running; forge-preview-change-8 is one it has never held.
	if err := os.MkdirAll(filepath.Join(state, "previews", "forge-preview-change-9"), 0o755); err != nil {
		t.Fatal(err)
	}
	hs := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: state, Previews: dk})
	client = workertest.Client(t, ca, workertest.Installation)
	forge = httptest.NewServer(worker.Previews{URL: hs.URL, Client: client}.Forward("forge-preview-change-7", "https"))
	t.Cleanup(forge.Close)
	return forge, hs.URL, client, func() seen { mu.Lock(); defer mu.Unlock(); return last }, dk
}

// The two hops change nothing the app sees: its Host, its path and query as
// sent, and the forge's own forwarding headers, never a visitor's. The
// worker's routing headers do not reach the app.
func TestAWorkerPreviewSeesTheVisitorsRequestThroughBothHops(t *testing.T) {
	t.Parallel()
	forge, _, _, got, _ := forwardedPreview(t)
	req, _ := http.NewRequest(http.MethodGet, forge.URL+"/a%2Fb/c?x=1&y=%20z", nil)
	req.Host = "change-7.preview.example"
	for h, v := range map[string]string{
		"X-Forwarded-For": "6.6.6.6", "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "http",
		worker.PreviewHostHeader: "evil.example", worker.PreviewTargetHeader: "/v1/checks",
		"Cookie": "session=1", "Authorization": "Bearer app-token",
	} {
		req.Header.Set(h, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	s := got()
	if s.Host != "change-7.preview.example" || s.Path != "/a%2Fb/c" || s.Query != "x=1&y=%20z" {
		t.Fatalf("the app saw %q %q %q", s.Host, s.Path, s.Query)
	}
	if s.Header.Get("X-Forwarded-For") != "127.0.0.1" || s.Header.Get("X-Forwarded-Host") != "change-7.preview.example" || s.Header.Get("X-Forwarded-Proto") != "https" {
		t.Fatalf("forwarding headers the forge did not set reached the app: %v", s.Header)
	}
	if s.Header.Get("Cookie") != "session=1" || s.Header.Get("Authorization") != "Bearer app-token" {
		t.Fatalf("the app's own headers did not pass: %v", s.Header)
	}
	if s.Header.Get(worker.PreviewHostHeader) != "" || s.Header.Get(worker.PreviewTargetHeader) != "" {
		t.Fatalf("the worker hop's headers reached the app: %v", s.Header)
	}
}

// A visitor's path is the app's, whatever it says: one that climbs toward
// the worker's own paths reaches the app as sent, and never the worker.
func TestAVisitorsPathNeverReachesTheWorker(t *testing.T) {
	t.Parallel()
	forge, _, _, got, _ := forwardedPreview(t)
	conn, err := net.Dial("tcp", forge.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /../../../v1/previews HTTP/1.1\r\nHost: change-7.preview.example\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || got().Path != "/../../../v1/previews" {
		t.Fatalf("status %d, the app saw %q", resp.StatusCode, got().Path)
	}
}

// Upgrades pass through both hops: a WebSocket reaches the app and talks.
func TestAWorkerPreviewCarriesAWebSocket(t *testing.T) {
	t.Parallel()
	forge, _, _, got, _ := forwardedPreview(t)
	u, _ := url.Parse(forge.URL)
	u.Scheme, u.Path = "ws", "/live"
	c, _, err := websocket.DefaultDialer.Dial(u.String(), http.Header{"Origin": {"https://change-7.preview.example"}, "Host": {"change-7.preview.example"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := c.ReadMessage()
	if err != nil || string(msg) != "echo hello" {
		t.Fatalf("%q %v", msg, err)
	}
	if got().Header.Get("Origin") != "https://change-7.preview.example" {
		t.Fatalf("the app saw origin %q", got().Header.Get("Origin"))
	}
}

// A forwarded request selects one preview's app port and nothing else: a
// name that is not a preview's is refused before Docker is asked, and a
// request without its target is refused.
func TestAForwardedRequestReachesOnlyAPreviewsPort(t *testing.T) {
	t.Parallel()
	_, workerURL, client, _, dk := forwardedPreview(t)
	for _, tc := range []struct {
		path, target string
		want         int
	}{
		{"/v1/previews/forge-db/http", "/", http.StatusBadRequest},
		{"/v1/previews/forge-env-app-main/http", "/", http.StatusBadRequest},
		{"/v1/previews/forge-preview-change-7/http", "", http.StatusBadRequest},
		{"/v1/previews/forge-preview-change-7/http", "http://elsewhere/", http.StatusBadRequest},
		{"/v1/previews/forge-preview-change-8/http", "/", http.StatusNotFound},
		{"/v1/previews/forge-preview-change-9/http", "/", http.StatusBadGateway},
	} {
		req, _ := http.NewRequest(http.MethodGet, workerURL+tc.path, nil)
		if tc.target != "" {
			req.Header.Set(worker.PreviewTargetHeader, tc.target)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %q: status %d, want %d", tc.path, tc.target, resp.StatusCode, tc.want)
		}
	}
	for _, line := range dk.commands("") {
		if line != "" && line != "port forge-preview-change-8" && line != "port forge-preview-change-9" {
			t.Errorf("Docker was asked %q", line)
		}
	}
}

// The naked door a probe uses (no public scheme) reaches the app with no
// forwarding headers at all, as a request to the app's own port would.
func TestTheNakedDoorCarriesNoForwardingHeaders(t *testing.T) {
	t.Parallel()
	_, workerURL, client, got, _ := forwardedPreview(t)
	naked := httptest.NewServer(worker.Previews{URL: workerURL, Client: client}.Forward("forge-preview-change-7", ""))
	defer naked.Close()
	req, _ := http.NewRequest(http.MethodGet, naked.URL+"/ready", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if v := got().Header.Get(h); v != "" {
			t.Errorf("the naked door carried %s: %q", h, v)
		}
	}
	if got().Path != "/ready" {
		t.Fatalf("the app saw %q", got().Path)
	}
}
