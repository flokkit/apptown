package docker_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// On a daemon with attached streams a step is the one command a Docker
// host's copies have always run: docker exec in a running container, or
// docker run of a throwaway one, -i only when it reads a copy, its
// variables passed by name.
func TestAStepWithStreamsIsOneCommand(t *testing.T) {
	t.Parallel()
	d := dockerfake.New()
	var got []string
	d.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
		got = append(got, strings.Join(c.Args, " "))
		if strings.Join(c.Env, " ") != "PGPASSWORD=secret" {
			t.Errorf("the variables reach the CLI's environment: %v", c.Env)
		}
		return nil, nil, true
	})
	dk := docker.Scope{Client: d}
	env := []string{"PGPASSWORD=secret"}
	if _, err := dk.RunStep(context.Background(), docker.Step{Exec: "pg", Env: env, Command: "pg_dump", Stdout: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := dk.RunStep(context.Background(), docker.Step{Image: "pg:16", Options: []string{"--network", "none"}, Env: env, Command: "pg_restore", Stdin: strings.NewReader("copy")}); err != nil {
		t.Fatal(err)
	}
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
	j := &jobDaemon{Daemon: dockerfake.New(), exit: exit}
	j.SetReply(func(args []string) ([]byte, error, bool) {
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
	j := newJobDaemon("0", "the dump")
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
