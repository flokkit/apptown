package docker

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Each command gets a private local endpoint. Disabling upstream keep-alive
// releases a session after its HTTP response, rather than letting idle
// connections starve a stats call with more containers than session slots.
func commandProxy(ctx context.Context, dial func(context.Context) (net.Conn, error)) (string, func() error, error) {
	dir, err := os.MkdirTemp("", "apptown-docker-")
	if err != nil {
		return "", nil, err
	}
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	connections := &commandConnections{conns: make(map[*commandConn]struct{})}
	proxyCtx, cancel := context.WithCancel(ctx)
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return connections.dial(ctx, proxyCtx, dial)
	}}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "docker"})
	proxy.Transport = transport
	server := &http.Server{Handler: proxy, BaseContext: func(net.Listener) context.Context { return proxyCtx }}
	go server.Serve(listener)
	return "unix://" + socket, func() error {
		defer os.RemoveAll(dir)
		done := make(chan struct{})
		go func() {
			// Stop admission before waiting: Transport dials can outlive requests,
			// and Server.Close does not wait for upstream (or hijacked) connections.
			connections.mu.Lock()
			connections.stopped = true
			conns := make([]*commandConn, 0, len(connections.conns))
			for conn := range connections.conns {
				conns = append(conns, conn)
			}
			connections.mu.Unlock()
			cancel()
			server.Close()
			transport.CloseIdleConnections()
			for _, conn := range conns {
				go conn.Close()
			}
			connections.pending.Wait()
			close(done)
		}()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil
}

// commandConnections owns dials until they fail or their connection's Close
// returns. Keeping a closing connection registered includes its session release.
type commandConnections struct {
	mu      sync.Mutex
	stopped bool
	conns   map[*commandConn]struct{}
	pending sync.WaitGroup
}

func (c *commandConnections) dial(ctx, lifetime context.Context, dial func(context.Context) (net.Conn, error)) (net.Conn, error) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil, context.Canceled
	}
	c.pending.Add(1)
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer stop()
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		c.pending.Done()
		return nil, err
	}
	tracked := &commandConn{Conn: conn, owner: c}
	c.mu.Lock()
	c.conns[tracked] = struct{}{}
	stopped := c.stopped
	c.mu.Unlock()
	if stopped {
		tracked.Close()
		return nil, context.Canceled
	}
	return tracked, nil
}

type commandConn struct {
	net.Conn
	owner  *commandConnections
	closed atomic.Bool
}

func (c *commandConn) Close() error {
	// Other closers need not wait here; pending includes the first Close until
	// it returns. This also lets transport cancellation proceed during release.
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := c.Conn.Close()
	c.owner.mu.Lock()
	delete(c.owner.conns, c)
	c.owner.mu.Unlock()
	c.owner.pending.Done()
	return err
}

// ReverseProxy half-closes upgraded streams to finish sending input without
// dropping the daemon's remaining output.
func (c *commandConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return nil
}
