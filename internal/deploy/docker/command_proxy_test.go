package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

// Hold the session release after the HTTP response is complete. Closing the
// downstream server or its idle transport cannot substitute for this release.
func TestCommandProxyWaitsForConnectionRelease(t *testing.T) {
	t.Parallel()
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				releasing, released := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(released) })
				var active atomic.Int32
				endpoint, cleanup, err := commandProxy(ctx, func(context.Context) (net.Conn, error) {
					client, daemon := net.Pipe()
					active.Add(1)
					go func() {
						defer daemon.Close()
						req, err := http.ReadRequest(bufio.NewReader(daemon))
						if err != nil {
							t.Error(err)
							return
						}
						req.Body.Close()
						io.WriteString(daemon, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK")
					}()
					return &countedConn{Conn: client, release: func() {
						close(releasing)
						<-released
						active.Add(-1)
					}}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cancel(); cleanup() })
				t.Cleanup(release)
				tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(endpoint, "unix://"))
				}}
				defer tr.CloseIdleConnections()
				response, err := (&http.Client{Transport: tr}).Get("http://docker/stats")
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				<-releasing
				done := make(chan error, 1)
				go func() { done <- cleanup() }()
				synctest.Wait()
				select {
				case err := <-done:
					release()
					t.Fatalf("cleanup returned before session release: %v", err)
				default:
				}
				if canceled {
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatalf("cleanup: %v", err)
					}
					release()
				} else {
					release()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if got := active.Load(); got != 0 {
						t.Fatalf("sessions remaining: %d", got)
					}
				}
				synctest.Wait()
			})
		})
	}
}

// A transport dial may finish after its request and after cleanup starts.
// Cleanup cancels it, but still owns any connection it returns.
func TestCommandProxyWaitsForPendingDial(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		dialing, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
		finishDial := sync.OnceFunc(func() { close(finish) })
		var active atomic.Int32
		endpoint, cleanup, err := commandProxy(t.Context(), func(ctx context.Context) (net.Conn, error) {
			close(dialing)
			select {
			case <-ctx.Done():
				close(canceled)
			case <-finish:
			}
			<-finish
			client, daemon := net.Pipe()
			daemon.Close()
			active.Add(1)
			return &countedConn{Conn: client, release: func() { active.Add(-1) }}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanup() })
		t.Cleanup(finishDial)
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(endpoint, "unix://"))
		}}
		defer tr.CloseIdleConnections()
		requestDone := make(chan struct{})
		go func() {
			defer close(requestDone)
			response, _ := (&http.Client{Transport: tr}).Get("http://docker/stats")
			if response != nil {
				response.Body.Close()
			}
		}()
		<-dialing
		done := make(chan error, 1)
		go func() { done <- cleanup() }()
		synctest.Wait()
		select {
		case err := <-done:
			finishDial()
			t.Fatalf("cleanup returned with a pending dial: %v", err)
		default:
		}
		<-canceled
		finishDial()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got := active.Load(); got != 0 {
			t.Fatalf("sessions remaining: %d", got)
		}
		<-requestDone
	})
}
