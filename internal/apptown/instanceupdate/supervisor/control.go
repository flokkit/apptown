package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/ids"
)

// Observation is a current observation, not a cached identity. An unavailable
// adapter must return an error rather than a zero/stopped observation.
type Observation struct {
	PID     int
	UID     uint32
	Running bool
}

// Observer binds peer identity to the enrolled installation. It cannot select
// a different target from a control request.
type Observer interface {
	Observe(context.Context, Registration) (Observation, error)
}

type Request struct {
	Protocol  int        `json:"protocol"`
	Operation string     `json:"operation"`
	Intention *Intention `json:"intention,omitempty"`
	Request   ids.ID     `json:"request,omitempty"`
}

type Status struct {
	State               string `json:"state"`
	Instance            ids.ID `json:"instance"`
	ConfigurationDigest string `json:"configurationDigest"`
	Adapter             string `json:"adapter"`
	Running             bool   `json:"running"`
	Phase               string `json:"phase"`
	Eligible            bool   `json:"eligible"`
	Reason              string `json:"reason"`
}

type Reply struct {
	Protocol    int          `json:"protocol"`
	Status      *Status      `json:"status,omitempty"`
	Transaction *Transaction `json:"transaction,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// Server authenticates kernel identity and current adapter observations before
// reading product attribution. U2 deliberately exposes no stop/install command.
type Server struct {
	Store    *Store
	Observer Observer
	Now      func() time.Time
}

func (s Server) status(ctx context.Context) (Status, error) {
	r := s.Store.Registration()
	out := Status{State: "enrolled", Instance: r.Instance, ConfigurationDigest: r.ConfigurationDigest, Adapter: r.Adapter, Phase: "idle", Reason: "Matched recovery, installation verification and worker compatibility are not qualified."}
	tx, err := s.Store.Transactions()
	if err != nil {
		return out, err
	}
	for _, t := range tx {
		if t.Phase != "cancelled" {
			out.Phase = t.Phase
		}
	}
	obs, err := s.Observer.Observe(ctx, r)
	if err != nil {
		return out, err
	}
	out.Running = obs.Running
	if err := s.Store.startupFailure(); err != nil {
		out.Phase = "startup-blocked"
		out.Reason = err.Error()
	}
	return out, nil
}

func (s Server) handle(ctx context.Context, c *net.UnixConn) Reply {
	reply := Reply{Protocol: Protocol}
	fail := func(err error) Reply { reply.Error = err.Error(); return reply }
	p, err := peer(c)
	if err != nil {
		return fail(err)
	}
	r := s.Store.Registration()
	// A host administrator has a distinct OS identity. A root-running daemon
	// must still match the observed registered process for daemon staging.
	hostAdmin := p.UID == 0
	obs, err := s.Observer.Observe(ctx, r)
	if err != nil {
		return fail(fmt.Errorf("registered process is unavailable: %w", err))
	}
	daemon := obs.Running && p.PID == obs.PID && p.UID == r.DaemonUID && obs.UID == r.DaemonUID
	if !hostAdmin && !daemon {
		return fail(errors.New("control peer is not the registered daemon or host administrator"))
	}
	raw, err := bufio.NewReader(io.LimitReader(c, 65537)).ReadBytes('\n')
	if err != nil {
		return fail(errors.New("incomplete control request"))
	}
	if len(raw) > 65536 {
		return fail(errors.New("control request exceeds limit"))
	}
	var in Request
	if err := decode(raw, &in); err != nil {
		return fail(err)
	}
	if in.Protocol != Protocol {
		return fail(errors.New("unsupported supervisor protocol"))
	}
	switch in.Operation {
	case "status":
		if in.Intention != nil || in.Request != ids.Nil {
			return fail(errors.New("status has unexpected request fields"))
		}
		st, err := s.status(ctx)
		if err != nil {
			return fail(err)
		}
		reply.Status = &st
	case "stage":
		if !daemon {
			return fail(errors.New("only the registered daemon can attribute a product request"))
		}
		if in.Intention == nil || in.Request != ids.Nil {
			return fail(errors.New("stage needs one complete intention"))
		}
		tx, err := s.Store.Stage(*in.Intention, clock.Or(s.Now)())
		if err != nil {
			return fail(err)
		}
		reply.Transaction = &tx
	case "cancel":
		if in.Intention != nil || in.Request == ids.Nil {
			return fail(errors.New("cancel needs one request UUID"))
		}
		tx, err := s.Store.Cancel(in.Request)
		if err != nil {
			return fail(err)
		}
		reply.Transaction = &tx
	default:
		return fail(errors.New("unsupported supervisor operation"))
	}
	return reply
}

// Serve owns the socket while the Store owns installation exclusion. Caller
// cancellation closes active connections and settles handlers before returning.
func (s Server) Serve(ctx context.Context) error {
	if s.Store == nil || s.Observer == nil {
		return errors.New("supervisor needs registered store and observer")
	}
	path := filepath.Join(s.Store.registration.StateDir, "control.sock")
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("control path is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer l.Close()
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = l.Close()
		case <-done:
		}
	}()
	// Serial bounded requests keep mutation order explicit. A peer cannot pin the
	// owner indefinitely; connection cancellation also releases the next request.
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err := c.SetDeadline(clock.Or(nil)().Add(5 * time.Second)); err != nil {
			_ = c.Close()
			return err
		}
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		opctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		reply := s.handle(opctx, c)
		_ = json.NewEncoder(c).Encode(reply) // A disconnected client retries the durable request UUID.
		cancel()
		stop()
		_ = c.Close()
	}
}

// Call uses an explicitly configured local endpoint. Absence of that socket is
// an unavailable supervisor, never evidence of an unmanaged installation.
func Call(ctx context.Context, socket string, in Request) (Reply, error) {
	var reply Reply
	if !canonical(socket) {
		return reply, errors.New("supervisor socket must be canonical")
	}
	if err := secureDirectory(filepath.Dir(socket)); err != nil {
		return reply, err
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return reply, err
	}
	defer c.Close()
	if err := c.SetDeadline(clock.Or(nil)().Add(5 * time.Second)); err != nil {
		return reply, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if err := json.NewEncoder(c).Encode(in); err != nil {
		return reply, err
	}
	raw, err := bufio.NewReader(io.LimitReader(c, 65537)).ReadBytes('\n')
	if err != nil {
		return reply, err
	}
	if len(raw) > 65536 {
		return reply, errors.New("supervisor reply exceeds limit")
	}
	if err := decode(raw, &reply); err != nil {
		return reply, err
	}
	if reply.Protocol != Protocol {
		return reply, errors.New("unsupported supervisor reply")
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	switch in.Operation {
	case "status":
		if reply.Status == nil || reply.Transaction != nil || reply.Status.Instance == ids.Nil || !hexDigest.MatchString(reply.Status.ConfigurationDigest) || reply.Status.State != "enrolled" {
			return reply, errors.New("incomplete supervisor status reply")
		}
	case "stage":
		if in.Intention == nil || reply.Transaction == nil || reply.Status != nil || reply.Transaction.Intention != *in.Intention {
			return reply, errors.New("supervisor returned another staged intention")
		}
	case "cancel":
		if reply.Transaction == nil || reply.Status != nil || reply.Transaction.Intention.Request != in.Request || reply.Transaction.Phase != "cancelled" {
			return reply, errors.New("supervisor did not confirm the requested cancellation")
		}
	}
	return reply, nil
}
