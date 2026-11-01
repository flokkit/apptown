package docker_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// On a daemon with attached streams a step is the one command a Docker
// host's copies have always run: docker exec in a running container, or
// docker run of a throwaway one, -i only when it reads a copy, its
// variables passed by name. A command whose output is the copy is framed,
// and the copy reaches the step without its trailer.
func TestAStepWithStreamsIsOneCommand(t *testing.T) {
	t.Parallel()
	d := newDockerFake()
	var got []string
	d.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
		got = append(got, strings.Join(c.Args, " "))
		if strings.Join(c.Env, " ") != "PGPASSWORD=secret" {
			t.Errorf("the variables reach the CLI's environment: %v", c.Env)
		}
		if c.Stdout != nil {
			_, _ = c.Stdout.Write(containerresulttest.Framed([]byte("the dump")))
		}
		return nil, nil, true
	})
	dk := docker.Scope{Client: d}
	env := []string{"PGPASSWORD=secret"}
	var dump bytes.Buffer
	if _, err := dk.RunStep(context.Background(), docker.Step{Exec: "pg", Env: env, Command: "pg_dump", Stdout: &dump}); err != nil || dump.String() != "the dump" {
		t.Fatalf("the copy %q, %v; want it whole without its trailer", dump.String(), err)
	}
	if _, err := dk.RunStep(context.Background(), docker.Step{Image: "pg:16", Options: []string{"--network", "none"}, Env: env, Command: "pg_restore", Stdin: strings.NewReader("copy")}); err != nil {
		t.Fatal(err)
	}
	// The stand-in daemon records the command inside the copy's frame.
	want := []string{"exec -e PGPASSWORD pg sh -c pg_dump", "run --rm -i --network none -e PGPASSWORD pg:16 sh -c pg_restore"}
	if !slices.Equal(got, want) {
		t.Fatalf("commands %q, want %q", got, want)
	}
}

// jobDaemon is a daemon reached without attached streams, recording what a
// job does: the input it is given, its exit status, its output.
type jobDaemon struct {
	*dockerfake.Daemon
	exit string
}

func newJobDaemon(exit string, output string) *jobDaemon {
	j := &jobDaemon{Daemon: newDockerFake(), exit: exit}
	j.SetReply(func(args []string) ([]byte, error, bool) {
		if j.IsDiskRead(args) {
			return nil, nil, false
		}
		switch args[0] {
		case "inspect":
			return []byte(`postgres:16-alpine` + "\t" + `["POSTGRES_USER=keeper","PATH=/bin"]`), nil, true
		case "create":
			return []byte("Unable to find image locally\njob-1\n"), nil, true
		case "wait":
			return []byte(j.exit + "\n"), nil, true
		case "logs":
			return []byte("pg_restore: done"), nil, true
		}
		return nil, nil, false
	})
	j.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
		if c.Args[0] != "cp" || c.Stdout == nil {
			return nil, nil, false
		}
		tw := tar.NewWriter(c.Stdout)
		_ = tw.WriteHeader(&tar.Header{Name: "forge-copy-out", Mode: 0o600, Size: int64(len(output)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(output))
		_ = tw.Close()
		return nil, nil, true
	})
	return j
}

// Through a proxy that carries no attached streams (Portainer's) a step
// runs as a job: an exec becomes a container of the running one's image
// and environment that shares its network and volumes; the input is copied
// in and the command reads it from there; the job is started and waited
// for; its output is copied out to the step's; it is removed. Its exit
// status, not the wait's, is the step's result.
func TestAStepWithoutStreamsIsAJob(t *testing.T) {
	t.Parallel()
	j := newJobDaemon("0", string(containerresulttest.Framed([]byte("the dump"))))
	dk := docker.Scope{Client: j, NoStreams: true}
	var out bytes.Buffer
	if _, err := dk.RunStep(context.Background(), docker.Step{Exec: "pg", Env: []string{"PGPASSWORD=secret"}, Command: "pg_dump", Stdout: &out, Stdin: strings.NewReader("in"), Name: "forge-copy-prod-1"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "the dump" {
		t.Fatalf("the step's output: %q", out.String())
	}
	var verbs []string
	var create []string
	for _, c := range j.Commands() {
		verbs = append(verbs, c[0])
		if c[0] == "create" {
			create = c
		}
	}
	if want := []string{"inspect", "create", "cp", "start", "wait", "logs", "cp", "rm"}; !slices.Equal(verbs, want) {
		t.Fatalf("the job's commands %v, want %v", verbs, want)
	}
	joined := strings.Join(create, " ")
	for _, want := range []string{"--name forge-copy-prod-1", "--network container:pg", "--volumes-from pg", "-e POSTGRES_USER", "-e PGPASSWORD", " postgres:16-alpine sh -c exec </tmp/forge-copy-in >/tmp/forge-copy-out\npg_dump"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the job is made without %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "secret") {
		t.Fatalf("a value is in the job's arguments: %s", joined)
	}
	cmds := j.Commands()
	if last := cmds[len(cmds)-1]; strings.Join(last, " ") != "rm -f -v job-1" {
		t.Fatalf("the job is not removed: %v", last)
	}
}

// A job whose command fails is a failed step, with its log, though the wait
// itself succeeded; and it is removed all the same.
func TestAJobThatFailsIsAFailedStep(t *testing.T) {
	t.Parallel()
	j := newJobDaemon("3", "")
	dk := docker.Scope{Client: j, NoStreams: true}
	out, err := dk.RunStep(context.Background(), docker.Step{Image: "alpine:3", Command: "false", Stdout: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || string(out) != "pg_restore: done" {
		t.Fatalf("a failed job: %q %v", out, err)
	}
	cmds := j.Commands()
	if last := cmds[len(cmds)-1]; last[0] != "rm" {
		t.Fatalf("the failed job is not removed: %v", last)
	}
}

// A copy whose end did not arrive (the daemon lost the tail of the
// container's stdout, and the command exited 0) fails the step, in both
// forms: the trailer is missing, or does not count what arrived.
func TestAStepWhoseCopyLostItsTailFails(t *testing.T) {
	t.Parallel()
	whole := containerresulttest.Framed([]byte("a dump with its last rows"))
	for _, lost := range [][]byte{whole[:len(whole)-40], append(append([]byte{}, whole[:10]...), whole[20:]...), nil} {
		d := newDockerFake()
		d.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
			_, _ = c.Stdout.Write(lost)
			return nil, nil, true
		})
		if _, err := (docker.Scope{Client: d}).RunStep(context.Background(), docker.Step{Exec: "pg", Command: "pg_dump", Stdout: io.Discard}); !errors.Is(err, containerresult.ErrCutShort) {
			t.Fatalf("streamed, %d bytes arrived: got %v; want the copy refused", len(lost), err)
		}
		j := newJobDaemon("0", string(lost))
		if _, err := (docker.Scope{Client: j, NoStreams: true}).RunStep(context.Background(), docker.Step{Exec: "pg", Command: "pg_dump", Stdout: io.Discard}); !errors.Is(err, containerresult.ErrCutShort) {
			t.Fatalf("as a job, %d bytes arrived: got %v; want the copy refused", len(lost), err)
		}
	}
}

// clientFunc is a Docker client that is one function.
type clientFunc func(docker.Command) ([]byte, error)

func (f clientFunc) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) { return f(c) }

// What a step sends the daemon is its command framed when its output is a
// copy, and as it is when it has none.
func TestAStepsCopyIsFramedOnTheWire(t *testing.T) {
	t.Parallel()
	var sent []string
	dk := docker.Scope{Client: clientFunc(func(c docker.Command) ([]byte, error) {
		sent = append(sent, c.Args[len(c.Args)-1])
		if c.Stdout != nil {
			_, _ = c.Stdout.Write(containerresulttest.Framed(nil))
		}
		return nil, nil
	})}
	if _, err := dk.RunStep(context.Background(), docker.Step{Exec: "pg", Argv: []string{"pg_dump", "-U", "it's"}, Stdout: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := dk.RunStep(context.Background(), docker.Step{Exec: "pg", Command: "pg_restore", Stdin: strings.NewReader("x")}); err != nil {
		t.Fatal(err)
	}
	if want := []string{containerresult.Framed(`'pg_dump' '-U' 'it'\''s'`), "pg_restore"}; !slices.Equal(sent, want) {
		t.Fatalf("sent %q, want %q", sent, want)
	}
}
