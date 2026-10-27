// apptown-update is installed outside the replaceable release and data trees.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/flokkit/apptown/internal/apptown/instanceupdate/supervisor"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/ids"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "apptown-update:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("apptown-update", flag.ContinueOnError)
	state := fs.String("state", "/var/lib/apptown-update", "registered external control directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("choose enroll, unit, serve, status, recover, run or run-container")
	}
	command := fs.Arg(0)
	if command == "unit" || command == "enroll" {
		if fs.NArg() != 2 {
			return errors.New("unit/enroll requires a host registration JSON file")
		}
		f, err := os.Open(fs.Arg(1))
		if err != nil {
			return err
		}
		defer f.Close()
		var r supervisor.Registration
		dec := json.NewDecoder(io.LimitReader(f, 65536))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil {
			return err
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return errors.New("unexpected registration data")
		}
		if command == "unit" {
			body, err := supervisor.ManagedUnit(r)
			if err != nil {
				return err
			}
			_, err = out.Write(body)
			return err
		}
		if r.StateDir != *state {
			return errors.New("registration and -state differ")
		}
		r.Format = supervisor.Protocol
		r.EnrolledAt = clock.Or(nil)()
		r.EnrolledByUID = uint32(os.Geteuid())
		if r.Instance == ids.Nil {
			r.Instance = ids.New(r.EnrolledAt)
		}
		r, err = supervisor.SealRegistration(r)
		if err != nil {
			return err
		}
		return supervisor.Enroll(ctx, r, supervisor.Host{})
	}
	if fs.NArg() != 1 {
		return errors.New("unexpected supervisor arguments")
	}
	switch command {
	case "run":
		return supervisor.RunDaemon(ctx, *state, supervisor.Host{})
	case "run-container":
		return supervisor.RunContainer(ctx, *state, supervisor.Host{})
	case "serve":
		if os.Geteuid() != 0 {
			return errors.New("supervisor requires host root administration")
		}
		s, err := supervisor.Open(*state)
		if err != nil {
			return err
		}
		defer s.Close()
		return (supervisor.Server{Store: s, Observer: supervisor.Host{}}).Serve(ctx)
	case "status", "recover":
		reply, err := supervisor.Call(ctx, filepath.Join(*state, "control.sock"), supervisor.Request{Protocol: supervisor.Protocol, Operation: "status"})
		if err == nil {
			if err := json.NewEncoder(out).Encode(reply.Status); err != nil {
				return err
			}
		} else {
			if os.Geteuid() != 0 {
				return err
			}
			s, openErr := supervisor.Open(*state)
			if openErr != nil {
				return errors.Join(err, openErr)
			}
			defer s.Close()
			tx, readErr := s.Transactions()
			if readErr != nil {
				return readErr
			}
			startupRefusal := ""
			if problem := s.OrdinaryStart(); problem != nil {
				startupRefusal = problem.Error()
			}
			if err := json.NewEncoder(out).Encode(struct {
				StartupRefusal string                   `json:"startupRefusal,omitempty"`
				State          string                   `json:"state"`
				Registration   supervisor.Registration  `json:"registration"`
				Transactions   []supervisor.Transaction `json:"transactions"`
			}{startupRefusal, "supervisor-unavailable", s.Registration(), tx}); err != nil {
				return err
			}
		}
		if command == "recover" {
			return errors.New("matched recovery is not installed; the external journal and startup fence are unchanged")
		}
		return nil
	default:
		return errors.New("unknown supervisor command")
	}
}
