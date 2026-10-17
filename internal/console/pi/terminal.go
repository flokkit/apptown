package pi

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"flokkit.io/forge/internal/console"

	"github.com/creack/pty"
)

// attachLocked holds a disposable tmux client on the session under a host
// PTY: killing it (detach, forged restart) leaves the tmux server and pi
// untouched inside the container. Caller holds attachMu.
func (r *Runtime) attachLocked(st *state, id string) error {
	st.mu.Lock()
	attached := st.ptmx != nil
	st.mu.Unlock()
	if attached {
		return nil
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(r.cfg.Binary, "exec", "-it", ContainerName(id),
		"tmux", "-u", "-f", console.Mount+"/tmux.conf", "new-session", "-A", "-s", console.TmuxSession, console.Mount+"/run-pi.sh")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("attaching pty: %w", err)
	}
	st.mu.Lock()
	st.ptmx, st.attachCmd = ptmx, cmd
	st.mu.Unlock()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				st.mu.Lock()
				st.ring = append(st.ring, chunk...)
				if len(st.ring) > ringSize {
					st.ring = st.ring[len(st.ring)-ringSize:]
				}
				for _, ch := range st.clients {
					select {
					case ch <- chunk:
					default: // slow viewer: drop rather than stall the PTY
					}
				}
				st.mu.Unlock()
			}
			if err != nil {
				// attach died (container stopped, pi exited, or we detached)
				st.mu.Lock()
				if st.ptmx == ptmx {
					st.ptmx, st.attachCmd = nil, nil
					for _, ch := range st.clients {
						close(ch)
					}
					st.clients = map[*view]chan []byte{}
				}
				st.mu.Unlock()
				_ = ptmx.Close()
				return
			}
		}
	}()
	return nil
}

// detach closes the PTY and kills the tmux CLIENT outright: it does not
// reliably exit on PTY-master close, and waiting on it wedges callers.
//
// Its viewers end with it: a terminal open on a stopped or removed agent
// reads the end, rather than waiting for a screen that will not come.
func (r *Runtime) detach(st *state) {
	st.mu.Lock()
	ptmx, cmd := st.ptmx, st.attachCmd
	st.ptmx, st.attachCmd = nil, nil
	for _, ch := range st.clients {
		close(ch)
	}
	st.clients = map[*view]chan []byte{}
	st.mu.Unlock()
	if ptmx != nil {
		_ = ptmx.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		go func() { _ = cmd.Wait() }()
	}
}

// Attach opens a viewer's window onto the session's screen: the ring
// replayed, then live output. Start must have run first.
func (r *Runtime) Attach(ctx context.Context, id string, size console.Size) (console.Terminal, error) {
	st := r.state(id)
	st.attachMu.Lock()
	err := r.attachLocked(st, id)
	st.attachMu.Unlock()
	if err != nil {
		return nil, err
	}
	v := &view{st: st, ch: make(chan []byte, 64)}
	st.mu.Lock()
	st.clients[v] = v.ch
	v.replay = append([]byte(nil), st.ring...)
	st.mu.Unlock()
	if size.Cols > 0 && size.Rows > 0 {
		_ = v.Resize(size)
	}
	return v, nil
}

// view is one viewer on the shared PTY.
type view struct {
	st     *state
	ch     chan []byte
	replay []byte
	closed bool
}

func (v *view) Read(p []byte) (int, error) {
	if len(v.replay) > 0 {
		n := copy(p, v.replay)
		v.replay = v.replay[n:]
		return n, nil
	}
	chunk, ok := <-v.ch
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}

func (v *view) Write(p []byte) (int, error) {
	v.st.mu.Lock()
	ptmx := v.st.ptmx
	v.st.mu.Unlock()
	if ptmx == nil {
		return 0, io.ErrClosedPipe
	}
	return ptmx.Write(p)
}

func (v *view) Resize(size console.Size) error {
	v.st.mu.Lock()
	ptmx := v.st.ptmx
	v.st.mu.Unlock()
	if ptmx == nil || size.Cols == 0 || size.Rows == 0 {
		return nil
	}
	return pty.Setsize(ptmx, &pty.Winsize{Cols: size.Cols, Rows: size.Rows})
}

func (v *view) Close() error {
	v.st.mu.Lock()
	if cur, ok := v.st.clients[v]; ok && cur == v.ch && !v.closed {
		delete(v.st.clients, v)
		close(v.ch)
	}
	v.closed = true
	v.st.mu.Unlock()
	return nil
}
