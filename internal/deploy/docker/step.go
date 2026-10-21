package docker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/strs"
)

// Step is one command of a data copy run against a daemon: in a running
// container (docker exec), or in a throwaway container of an image with
// docker run options (a network, a volume). Its input, when it reads one,
// is a file on stdin; its output, when it writes one, is a file from
// stdout. Env names the variables it is given, NAME=value: the values reach
// the container by name only, never as arguments.
type Step struct {
	Exec    string   // the running container to run in
	Image   string   // else: a throwaway container of this image
	Options []string // docker run options for Image (--network, -v, --volumes-from)
	Env     []string
	// Command is the shell command; Argv, when set instead, runs as it is.
	Command string
	Argv    []string
	Stdin   io.Reader
	Stdout  io.Writer
	// Name names the container a job makes (a person finds a job a stopped
	// AppTown left by it, and by its labels); "" lets the daemon name it.
	Name string
}

// argv is the step as one docker command with attached streams: exactly
// the commands a Docker host's copies have always run.
func (s Step) argv() []string {
	var flags []string
	for _, kv := range s.Env {
		name, _, _ := strings.Cut(kv, "=")
		flags = append(flags, "-e", name)
	}
	cmd := s.Argv
	if cmd == nil {
		cmd = []string{"sh", "-c", s.Command}
	}
	if s.Exec != "" {
		args := []string{"exec"}
		if s.Stdin != nil {
			args = append(args, "-i")
		}
		args = append(args, flags...)
		return append(append(args, s.Exec), cmd...)
	}
	args := []string{"run", "--rm"}
	if s.Stdin != nil {
		args = append(args, "-i")
	}
	args = append(args, s.Options...)
	args = append(args, flags...)
	return append(append(args, s.Image), cmd...)
}

// RunStep runs a step. A daemon reached with attached streams runs it as
// one command. One reached through a proxy that carries only plain
// requests (Portainer's, Scope.NoStreams) runs it as a job: a container
// made for it (an exec becomes a container of the running one's image, its
// environment, sharing its network and its volumes), its input copied in,
// started, waited for, its exit status read, its output copied out, and
// removed whatever happened. The output is what the error carries: stderr
// for a streamed step, the job's log for a job.
func (dk Scope) RunStep(ctx context.Context, s Step) ([]byte, error) {
	if !dk.NoStreams {
		return dk.Command(ctx, Command{Args: s.argv(), Stdin: s.Stdin, Stdout: s.Stdout, Env: s.Env})
	}
	return dk.runJob(ctx, s)
}

const (
	jobIn  = "/tmp/forge-copy-in"
	jobOut = "/tmp/forge-copy-out"
)

func (dk Scope) runJob(ctx context.Context, s Step) ([]byte, error) {
	image, options, env := s.Image, s.Options, s.Env
	if s.Exec != "" {
		// What exec sees: the container's image and environment, its
		// network and its volumes.
		raw, err := dk.Run(ctx, "inspect", "--format", "{{.Config.Image}}\t{{json .Config.Env}}", s.Exec)
		if err != nil {
			return raw, fmt.Errorf("reading how %s was started: %w", s.Exec, err)
		}
		img, envJSON, _ := strings.Cut(strings.TrimSpace(string(raw)), "\t")
		var own []string
		if err := json.Unmarshal([]byte(envJSON), &own); err != nil || img == "" {
			return raw, fmt.Errorf("reading how %s was started: %q", s.Exec, raw)
		}
		image, options = img, []string{"--entrypoint", "", "--network", "container:" + s.Exec, "--volumes-from", s.Exec}
		env = append(own, s.Env...)
	}
	cmd := s.Command
	if s.Argv != nil {
		cmd = shellJoin(s.Argv)
	}
	redirect := ""
	if s.Stdin != nil {
		redirect += " <" + jobIn
	}
	if s.Stdout != nil {
		redirect += " >" + jobOut
	}
	script := "exec" + redirect + "\n" + cmd
	var flags []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		flags = append(flags, "-e", name)
	}
	create := []string{"create"}
	if s.Name != "" {
		create = append(create, "--name", s.Name)
	}
	create = append(create, dk.Labels("copy", "job")...)
	create = append(create, options...)
	create = append(create, flags...)
	create = append(create, image, "sh", "-c", script)
	out, err := dk.Command(ctx, Command{Args: create, Env: env})
	if err != nil {
		return out, fmt.Errorf("making the job: %w: %s", err, strs.Truncate(strings.TrimSpace(string(out)), 300))
	}
	id := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(id, '\n'); i >= 0 {
		id = id[i+1:] // a pull's progress comes first
	}
	defer func() {
		_, _ = dk.Run(context.WithoutCancel(ctx), "rm", "-f", id)
	}()
	if s.Stdin != nil {
		dir, err := os.MkdirTemp("", "apptown-job-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		in := filepath.Join(dir, "apptown-copy-in")
		f, err := os.Create(in)
		if err != nil {
			return nil, err
		}
		_, cerr := io.Copy(f, s.Stdin)
		if err := errors.Join(cerr, f.Close()); err != nil {
			return nil, err
		}
		if out, err := dk.Run(ctx, "cp", in, id+":"+jobIn); err != nil {
			return out, fmt.Errorf("giving the job its input: %w", err)
		}
	}
	if out, err := dk.Run(ctx, "start", id); err != nil {
		return out, fmt.Errorf("starting the job: %w", err)
	}
	waited, err := dk.Run(ctx, "wait", id)
	if err != nil {
		return waited, fmt.Errorf("waiting for the job: %w", err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(waited)))
	if err != nil {
		return waited, fmt.Errorf("the job's exit status is not known: %q", waited)
	}
	logs, _ := dk.Run(ctx, "logs", id)
	if code != 0 {
		return logs, fmt.Errorf("exit status %d", code)
	}
	if s.Stdout != nil {
		// docker cp to stdout is a tar holding the one file, streamed into
		// the step's output as it comes.
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			err := untarOne(pr, s.Stdout)
			_, _ = io.Copy(io.Discard, pr)
			done <- err
		}()
		out, err := dk.Command(ctx, Command{Args: []string{"cp", id + ":" + jobOut, "-"}, Stdout: pw})
		pw.CloseWithError(err)
		if uerr := <-done; err == nil {
			err = uerr
		}
		if err != nil {
			return out, fmt.Errorf("reading the job's output: %w", err)
		}
	}
	return logs, nil
}

// shellJoin quotes each argument for sh.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// untarOne writes the first file of a tar stream to w: docker cp of one
// file to stdout is a tar holding that file.
func untarOne(r io.Reader, w io.Writer) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("the job wrote no output")
			}
			return err
		}
		if h.Typeflag == tar.TypeReg {
			_, err := io.Copy(w, tr)
			return err
		}
	}
}
