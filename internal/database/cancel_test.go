package database_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/poll"
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
