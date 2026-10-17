package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// The transport under the CLI never replays a mutation either. Go's HTTP/2
// transport sends a request again on its own after a peer's
// RST_STREAM(PROTOCOL_ERROR), which does not prove the peer did nothing:
// here the server acts on the first POST, resets the stream, and would
// answer a second with success. The mutation is sent once and is an error.
func TestATransportNeverReplaysAMutation(t *testing.T) {
	// The test server's certificate and a client that trusts it and would
	// speak HTTP/2 to it.
	cert := httptest.NewUnstartedServer(nil)
	cert.EnableHTTP2 = true
	cert.StartTLS()
	defer cert.Close()
	cfg := cert.TLS.Clone()
	cfg.NextProtos = []string{"h2", "http/1.1"}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	effects := 0
	act := func() int { mu.Lock(); defer mu.Unlock(); effects++; return effects }
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveOnce(conn.(*tls.Conn), act)
		}
	}()
	prev := api
	defer func() { api = prev }()
	api = &client{base: "https://" + ln.Addr().String(), actor: "tester", http: cert.Client(), wait: func(time.Duration) {},
		getenv: func(string) string { return "" }}
	_, err = api.request("POST", "/projects/shop/changes", map[string]any{"title": "x"})
	mu.Lock()
	defer mu.Unlock()
	if err == nil || effects != 1 {
		t.Fatalf("a mutation whose stream was reset after it acted: err %v, effects %d; want an error and one effect", err, effects)
	}
}

// serveOnce answers one connection: over HTTP/2 it acts on each complete
// request, resetting the first one's stream with PROTOCOL_ERROR and
// answering later ones 200; over HTTP/1.1 it acts and drops the connection
// before answering.
func serveOnce(conn *tls.Conn, act func() int) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second)) // a wrong turn fails the test, never hangs it
	if err := conn.Handshake(); err != nil {
		return
	}
	if conn.ConnectionState().NegotiatedProtocol != "h2" {
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err == nil {
			_, _ = io.Copy(io.Discard, req.Body)
			act()
		}
		return
	}
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(conn, preface); err != nil {
		return
	}
	fr := http2.NewFramer(conn, conn)
	if fr.WriteSettings() != nil {
		return
	}
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			return
		}
		ended := false
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				_ = fr.WriteSettingsAck()
			}
		case *http2.HeadersFrame:
			ended = f.StreamEnded()
		case *http2.DataFrame:
			ended = f.StreamEnded()
		}
		if !ended {
			continue
		}
		stream := f.Header().StreamID
		if act() == 1 {
			_ = fr.WriteRSTStream(stream, http2.ErrCodeProtocol)
			continue
		}
		var block bytes.Buffer
		enc := hpack.NewEncoder(&block)
		_ = enc.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
		_ = enc.WriteField(hpack.HeaderField{Name: "content-type", Value: "application/json"})
		_ = fr.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, BlockFragment: block.Bytes(), EndHeaders: true})
		_ = fr.WriteData(stream, true, []byte(`{"ok":true}`))
	}
}

// Sending a mutation over HTTP/1.1 leaves the client's own transport as it
// was: later mutations and reads over HTTPS still work, and reads keep
// HTTP/2.
func TestMutationsLeaveTheClientsTransportWorking(t *testing.T) {
	var mu sync.Mutex
	protos := map[string][]int{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protos[r.Method] = append(protos[r.Method], r.ProtoMajor)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	prev := api
	defer func() { api = prev }()
	api = &client{base: srv.URL, actor: "tester", http: srv.Client(), wait: func(time.Duration) {}, getenv: func(string) string { return "" }}
	for _, method := range []string{"POST", "POST", "GET"} {
		if _, err := api.request(method, "/projects/shop/repo", nil); err != nil {
			t.Fatalf("%s after a mutation: %v", method, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(protos["POST"]) != "[1 1]" || fmt.Sprint(protos["GET"]) != "[2]" {
		t.Fatalf("protocols used: %v; want mutations on HTTP/1.1 and the read on HTTP/2", protos)
	}
}
