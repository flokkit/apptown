package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// Under TLS, which pgx puts over the connection it dialed, the marker still
// finds the dialed connection, and a write a cancellation's deadline stops
// is still the cancellation and still a network timeout.
func TestACancelledWriteUnderTLSIsTheCancellation(t *testing.T) {
	t.Parallel()
	authority := trusttest.New(t, "database")
	_, _, cert := authority.Server(t, "db.test")
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(authority.PEM())
	near, far := net.Pipe()
	t.Cleanup(func() { near.Close(); far.Close() })
	served := make(chan error, 1)
	go func() { served <- tls.Server(far, &tls.Config{Certificates: []tls.Certificate{cert}}).Handshake() }()
	dialed := &cancellable{Conn: near}
	conn := tls.Client(dialed, &tls.Config{RootCAs: roots, ServerName: "db.test"})
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if beneath(conn) != dialed {
		t.Fatal("the marker does not find the dialed connection under TLS")
	}
	cause := context.Canceled
	dialed.cause.Store(&cause)
	if err := conn.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := conn.Write([]byte("SELECT 1"))
	var ne net.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("a write a cancellation stopped, under TLS: %v", err)
	}
}

// A timeout with no cancellation behind it, and any other failure, keep
// their own meaning.
func TestOnlyACancellationsTimeoutIsTheCancellation(t *testing.T) {
	t.Parallel()
	near, far := net.Pipe()
	t.Cleanup(func() { near.Close(); far.Close() })
	c := &cancellable{Conn: near}
	if err := c.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("x")); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("a timeout nobody cancelled: %v", err)
	}
	if err := c.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	cause := context.Canceled
	c.cause.Store(&cause)
	far.Close()
	if _, err := c.Write([]byte("x")); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("a closed connection during a cancellation: %v", err)
	}
}
