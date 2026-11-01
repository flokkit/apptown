package instancebackup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// The control socket: the running AppTown's door for its host's
// administrator, <data>/apptownd.sock, mode 0600. It serves one request, a
// whole-AppTown backup (backup.go), which only the running AppTown
// can take: it stops its deployments' parts under its own claims and holds
// its own new work. Who may ask is who may open the socket, the data
// directory's owner: whoever can read the data directory can already read
// every secret in it, so the socket adds no authority. The receipt names
// the host's administrator, with the user the command says it runs as.
//
// One request per connection: a JSON line, then the reply as JSON lines,
// each step as it starts ({"say"}) and one end ({"report"} or {"error"}).
// The connection closing ends the backup (its context), which releases
// whatever it holds.

// controlSocket is the socket's name in the data directory.
const controlSocket = "apptownd.sock"

// ControlRequest is what `apptownd backup` asks.
type ControlRequest struct {
	Op         string `json:"op"` // "backup"
	Archive    string `json:"archive"`
	Passphrase string `json:"passphrase"`
	Store      bool   `json:"store"`
	User       string `json:"user"` // the operating-system user the command runs as, self-reported
}

// ControlReply is one line of the reply.
type ControlReply struct {
	Say    string  `json:"say,omitempty"`
	Report *Report `json:"report,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// controlSocketPath is where a data directory's control socket is.
func controlSocketPath(dataDir string) string { return filepath.Join(dataDir, controlSocket) }

// ServeControl serves the control socket until ctx ends. A socket that
// cannot be made is logged and AppTown runs on without it: `apptownd
// backup` then says it cannot reach AppTown.
func (s *Service) ServeControl(ctx context.Context) error {
	path := controlSocketPath(s.deps.DataDir)
	// One apptownd per database holds this data directory: a socket left
	// here is a stopped one's.
	_ = os.Remove(path)
	l, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		log.Printf("control socket: %v; `apptownd backup` cannot reach this AppTown", err)
		<-ctx.Done()
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		log.Printf("control socket: %v; closed, so only its owner can ever reach it", err)
		<-ctx.Done()
		return nil
	}
	var wg sync.WaitGroup
	go func() { <-ctx.Done(); _ = l.Close() }()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("control socket: %v; `apptownd backup` cannot reach this AppTown", err)
				_ = l.Close()
				<-ctx.Done() // AppTown runs on without it
			}
			wg.Wait()
			_ = os.Remove(path)
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.serveControl(ctx, conn)
		}()
	}
}

func (s *Service) serveControl(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := bufio.NewReader(conn)
	line, err := in.ReadBytes('\n')
	if err != nil {
		return
	}
	var req ControlRequest
	enc := json.NewEncoder(conn)
	var mu sync.Mutex
	reply := func(r ControlReply) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(r)
	}
	if err := json.Unmarshal(line, &req); err != nil {
		reply(ControlReply{Error: "not a request: " + err.Error()})
		return
	}
	// The asker going away ends the backup.
	go func() {
		_, _ = io.Copy(io.Discard, in)
		cancel()
	}()
	switch req.Op {
	case "backup":
		actor := "the host's administrator (apptownd backup)"
		if req.User != "" {
			actor = fmt.Sprintf("the host's administrator, %s by its own account (apptownd backup)", req.User)
		}
		report, err := s.Backup(ctx, Request{Archive: req.Archive, Passphrase: req.Passphrase, ToStore: req.Store, Actor: actor,
			say: func(line string) { reply(ControlReply{Say: line}) }})
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				err = fmt.Errorf("%w (the backup was stopped)", err)
			}
			log.Printf("backup of AppTown: %v", err)
			reply(ControlReply{Error: err.Error()})
			return
		}
		reply(ControlReply{Report: &report})
	default:
		reply(ControlReply{Error: fmt.Sprintf("unknown request %q", req.Op)})
	}
}

// AskControl sends one request to the AppTown whose data directory is
// dataDir and calls say for each step; it returns the report, or the error
// AppTown answered, or that AppTown could not be reached.
func AskControl(ctx context.Context, dataDir string, req ControlRequest, say func(string)) (*Report, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", controlSocketPath(dataDir))
	if err != nil {
		return nil, fmt.Errorf("cannot reach the AppTown running on %s (%v): the backup is taken by the running AppTown; start it, or name its data directory with -data", dataDir, err)
	}
	defer conn.Close()
	go func() { <-ctx.Done(); _ = conn.Close() }()
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(conn)
	for {
		var r ControlReply
		if err := dec.Decode(&r); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("AppTown stopped answering before the backup ended: %w", err)
		}
		switch {
		case r.Error != "":
			return nil, errors.New(r.Error)
		case r.Report != nil:
			return r.Report, nil
		case say != nil:
			say(r.Say)
		}
	}
}
