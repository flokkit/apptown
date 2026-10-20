package localtls

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/clock"
)

// firstByteWait bounds how long a new connection may take to send its
// first byte before it is closed; each connection waits on its own, so a
// silent one never holds up the others.
const firstByteWait = 10 * time.Second

// Listener serves HTTPS and plain HTTP on one port: a connection that opens
// with a TLS handshake is handed out as a *tls.Conn with config (so
// net/http sees it as TLS), and any other is read as one HTTP request and
// answered with a 308 to the same address over https.
func Listener(inner net.Listener, config *tls.Config) net.Listener {
	l := &listener{inner: inner, config: config, conns: make(chan net.Conn), done: make(chan struct{})}
	go l.acceptLoop()
	return l
}

type listener struct {
	inner  net.Listener
	config *tls.Config
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func (l *listener) acceptLoop() {
	for {
		c, err := l.inner.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			l.mu.Lock()
			l.err = err
			l.mu.Unlock()
			l.Close()
			return
		}
		go l.classify(c)
	}
}

func (l *listener) classify(c net.Conn) {
	_ = c.SetReadDeadline(clock.System().Add(firstByteWait)) // a socket deadline: the kernel's clock
	r := bufio.NewReader(c)
	first, err := r.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}
	pc := &peekedConn{Conn: c, r: r}
	if first[0] == 0x16 { // a TLS handshake record
		select {
		case l.conns <- tls.Server(pc, l.config):
		case <-l.done:
			_ = c.Close()
		}
		return
	}
	redirect(pc, r)
}

// redirect answers one plain HTTP request with a 308 to https and closes.
func redirect(c net.Conn, r *bufio.Reader) {
	defer c.Close()
	_ = c.SetDeadline(clock.System().Add(firstByteWait)) // a socket deadline: the kernel's clock
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	host := req.Host
	if host == "" || strings.ContainsAny(host, " \r\n\t/\\@") {
		host = "localhost"
	}
	resp := &http.Response{
		StatusCode: http.StatusPermanentRedirect, ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Location": {"https://" + host + req.URL.RequestURI()}, "Connection": {"close"}},
		Close:  true,
	}
	_ = resp.Write(c)
}

type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.err != nil {
			return nil, l.err
		}
		return nil, net.ErrClosed
	}
}

func (l *listener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.inner.Close()
	})
	return err
}

func (l *listener) Addr() net.Addr { return l.inner.Addr() }
