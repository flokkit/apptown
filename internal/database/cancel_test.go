package database_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/poll"
)

// pgxReadsBehind is a connection whose writes, once armed, finish only
// after pgx has read the server's reply in the background and is waiting in
// another read: pgx starts that background reader when a write takes over
// 15 ms (as on a loaded machine), and it reads on until it is stopped. The
// waiting read is the one a cancellation's deadline stops. It reports a
// read stopped by a deadline.
type pgxReadsBehind struct {
	net.Conn
	armed             *atomic.Bool
	pending, returned *atomic.Int64
	timedOut          chan struct{}
	once              *atomic.Bool
}

func (c pgxReadsBehind) Write(p []byte) (int, error) {
	before := c.returned.Load()
	n, err := c.Conn.Write(p)
	if err == nil && c.armed.Load() && !poll.Until(context.Background(), 10*time.Second, time.Millisecond, func() bool {
		return c.returned.Load() > before && c.pending.Load() > 0
	}) {
		return n, errors.New("pgx did not read behind the write") // a hang guard
	}
	return n, err
}

func (c pgxReadsBehind) Read(p []byte) (int, error) {
	c.pending.Add(1)
	n, err := c.Conn.Read(p)
	c.pending.Add(-1)
	if n > 0 {
		c.returned.Add(1)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() && c.once.CompareAndSwap(false, true) {
		close(c.timedOut)
	}
	return n, err
}

// A query cancelled while its rows are read, after a slow write, does not
// leave an error behind for the next query on its connection. (22 Oct: the
// next query failed with "timeout: read tcp ...: i/o timeout" on a loaded
// CI runner, although nothing cancelled it.)
func TestACancelledQueryLeavesNoErrorForTheNextOne(t *testing.T) {
	t.Parallel()
	var armed, once atomic.Bool
	var pending, returned atomic.Int64
	timedOut := make(chan struct{})
	db := suite.NewWith(t, func(c net.Conn) net.Conn {
		return pgxReadsBehind{c, &armed, &pending, &returned, timedOut, &once}
	})
	// One connection, so the next query gets the cancelled one's.
	db.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armed.Store(true)
	rows, err := db.Pool().QueryContext(ctx, "SELECT generate_series(1, 3)")
	armed.Store(false)
	if err != nil {
		t.Fatal(err)
	}
	// Holding a row's raw bytes keeps database/sql from closing the rows
	// when the context is cancelled, so the query is still being read when
	// pgx handles the cancellation.
	var raw sql.RawBytes
	if !rows.Next() || rows.Scan(&raw) != nil {
		t.Fatalf("no first row: %v", rows.Err())
	}
	if pending.Load() == 0 {
		t.Fatal("no read waits behind the query")
	}
	cancel()
	select {
	case <-timedOut:
	case <-time.After(10 * time.Second): // a hang guard
		t.Fatal("the cancellation stopped no read")
	}
	_ = rows.Close()
	var one int
	if err := db.Pool().QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("the next query on a context nobody cancelled: %d %v", one, err)
	}
}

// writeCutByCancel is a connection whose next write, once armed, sends one
// byte and then waits until a cancellation's deadline is set on the
// connection before sending the rest, which the deadline then stops: a
// statement written slowly (as on a loaded machine) when its caller
// cancels. One byte already sent means pgx cannot retry the write on
// another connection.
type writeCutByCancel struct {
	net.Conn
	armed    *atomic.Bool
	started  chan struct{} // closed once the first byte is sent
	deadline chan struct{}
	once     *atomic.Bool
}

func (c writeCutByCancel) Write(p []byte) (int, error) {
	if len(p) < 2 || !c.armed.CompareAndSwap(true, false) {
		return c.Conn.Write(p)
	}
	n, err := c.Conn.Write(p[:1])
	if err != nil {
		return n, err
	}
	close(c.started)
	select {
	case <-c.deadline:
	case <-time.After(10 * time.Second): // a hang guard
		return n, errors.New("the cancellation set no deadline")
	}
	m, err := c.Conn.Write(p[1:])
	return n + m, err
}

func (c writeCutByCancel) SetDeadline(t time.Time) error {
	err := c.Conn.SetDeadline(t)
	if !t.IsZero() && c.once.CompareAndSwap(false, true) {
		close(c.deadline)
	}
	return err
}

// A statement whose write the caller's cancellation cuts short fails with
// the cancellation, not with the network timeout the cancellation caused:
// pgx turns a deadline into the context's error when it reads, but not when
// it writes. (22 Oct: a cancelled preview request returned "write failed:
// write tcp ...: i/o timeout" where its caller looked for context.Canceled.)
func TestACancelledWriteFailsWithTheCancellation(t *testing.T) {
	t.Parallel()
	var armed, once atomic.Bool
	started, deadline := make(chan struct{}), make(chan struct{})
	db := suite.NewWith(t, func(c net.Conn) net.Conn {
		return writeCutByCancel{c, &armed, started, deadline, &once}
	})
	db.Pool().SetMaxOpenConns(1)
	// The connection is open and idle before the statement is armed.
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armed.Store(true)
	go func() {
		// Cancelled while the statement is being written: the armed write
		// has sent its first byte and waits for the cancellation.
		<-started
		cancel()
	}()
	var one int
	err := db.SQL().QueryRowContext(ctx, "SELECT 1").Scan(&one)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a statement its caller cancelled while it was written: %v", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("the network's own words are lost: %v", err)
	}
}
