package maintenance

import (
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
)

func TestImageSweepRequiresAValidExpiredAge(t *testing.T) {
	t.Parallel()
	daemon := dockerfake.New()
	now := time.Now()
	rows := []string{
		"forge-conform:short\t?",
		"forge-conform:missing",
		"forge-conform:recent\t" + now.Add(-20*time.Hour).In(time.FixedZone("PST", -8*60*60)).Format("2006-01-02 15:04:05 -0700 MST"),
		"forge-conform:old\t" + now.Add(-48*time.Hour).UTC().Format("2006-01-02 15:04:05 -0700 MST"),
	}
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "image" && args[1] == "ls" {
			return []byte(strings.Join(rows, "\n")), nil, true
		}
		return nil, nil, false
	})
	a := &Reaper{docker: docker.Scope{Client: daemon}}
	if got := a.Images(context.Background(), false); len(got) != 1 || got[0] != "forge-conform:old" {
		t.Fatalf("age selection: %v", got)
	}
	removed := daemon.Matching("rmi ")
	if len(removed) != 1 || strings.Join(removed[0], " ") != "rmi forge-conform:old" {
		t.Fatalf("removed images: %v", removed)
	}
}

// refusalSpy stands for no local Docker, as docker.Refusing does, and
// records every call that reaches it.
type refusalSpy struct {
	docker.Refusing
	asked [][]string
}

func (s *refusalSpy) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	s.asked = append(s.asked, c.Args)
	return nil, docker.ErrNoLocalBackend
}

// On a forge with no local Docker, the reapers of its containers, images
// and cache answer from the mode: they remove nothing and ask nothing.
func TestTheDockerReapersDoNothingWithNoLocalDocker(t *testing.T) {
	t.Parallel()
	spy := &refusalSpy{}
	r := &Reaper{docker: docker.Scope{Client: spy}}
	for name, reap := range map[string]func(context.Context, bool) []string{"containers": r.Containers, "images": r.Images, "cache": r.Cache} {
		if got := reap(context.Background(), false); len(got) != 0 {
			t.Fatalf("%s reaped %v", name, got)
		}
	}
	if len(spy.asked) != 0 {
		t.Fatalf("the refusing client was asked: %v", spy.asked)
	}
}

// A deleted project's runs are gone with it; their build images are
// reaped, not kept forever as if their owners might still need them. A
// forge that cannot read its records keeps every image. A change's are
// Preview's, whatever its record says.
func TestImageSweepReapsImagesWhoseOwnerNoLongerExists(t *testing.T) {
	t.Parallel()
	for _, readable := range []bool{true, false} {
		daemon := dockerfake.New()
		daemon.SetReply(func(args []string) ([]byte, error, bool) {
			if len(args) > 1 && args[0] == "image" && args[1] == "ls" {
				return []byte("forge-preview-build-forge-preview-run-00000000-002a-7000-8000-000000000000:latest\nforge-preview-build-forge-preview-change-00000000-0009-7000-8000-000000000000-web:latest"), nil, true
			}
			return nil, nil, false
		})
		db := databaseSuite.New(t)
		if !readable {
			_ = db.Close()
		}
		r := &Reaper{db: db, docker: docker.Scope{Client: daemon}}
		got := r.Images(context.Background(), false)
		if readable && (len(got) != 1 || got[0] != "forge-preview-build-forge-preview-run-00000000-002a-7000-8000-000000000000:latest") || !readable && len(got) != 0 {
			t.Fatalf("readable=%v: reaped %v", readable, got)
		}
	}
}

// A container's age is judged on the forge's clock, which stamped its
// forge.created label: a step's container the forge labelled ahead of this
// machine's clock is reaped once the forge's clock is past the idle limit,
// whatever the machine's says. (Its step, 424242, does not exist: finished.)
// A preview of the same age is not the age reaper's: the last look at it
// decides (preview.Service.Tend).
func TestAContainersAgeIsJudgedOnTheForgesClock(t *testing.T) {
	t.Parallel()
	forgeNow := time.Now().Add(72 * time.Hour).UTC()
	created := forgeNow.Add(-3 * time.Hour)
	daemon := dockerfake.New()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "ps" {
			return []byte("forge-step-00000006-7932-7000-8000-000000000000\tstep\t" + created.Format(time.RFC3339) + "\t00000006-7932-7000-8000-000000000000\tc1\n" +
				"forge-preview-p\tpreview\t" + created.Format(time.RFC3339) + "\tforge-preview-p\tc2"), nil, true
		}
		return nil, nil, false
	})
	sessions := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: t.TempDir()}})
	r := New(databaseSuite.New(t), Dependencies{Docker: docker.Scope{Client: daemon}, Sessions: sessions, Now: func() time.Time { return forgeNow }}, t.TempDir())
	if got := r.Containers(context.Background(), true); len(got) != 1 || got[0] != "forge-step-00000006-7932-7000-8000-000000000000" {
		t.Fatalf("would reap %v, want the step's container three hours old on the forge's clock, and no preview", got)
	}
}

// An image a container still uses stays, and the sweep says nothing about
// it: it is tried again each sweep, and named only once it goes.
// Not parallel: it reads the process's log.
func TestAnImageInUseIsKeptQuietly(t *testing.T) {
	daemon := dockerfake.New()
	old := time.Now().Add(-48 * time.Hour).UTC().Format("2006-01-02 15:04:05 -0700 MST")
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		switch {
		case len(args) > 1 && args[0] == "image" && args[1] == "ls":
			return []byte("forge-conform:used\t" + old), nil, true
		case args[0] == "rmi":
			return []byte("Error response from daemon: conflict: unable to remove repository reference \"forge-conform:used\" (must force) - container 0123abcd is using its referenced image 4567ef"), errors.New("exit status 1"), true
		}
		return nil, nil, false
	})
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	got := (&Reaper{docker: docker.Scope{Client: daemon}}).Images(context.Background(), false)
	log.SetOutput(prev)
	if len(got) != 0 || buf.Len() != 0 {
		t.Fatalf("an image in use was reported: reaped %v, logged %q", got, buf.String())
	}
}

// The toolchain sweep visits each project's cache volume and the shared one
// older steps wrote, never a chat's own cache (which goes with its chat).
//
// Kills: a sweep of forge-cache alone, which never reaped what steps now
// write to their project's volume.
func TestTheCacheSweepVisitsEveryProjectsCache(t *testing.T) {
	t.Parallel()
	daemon := dockerfake.New()
	var listed []string
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "volume ls"):
			return []byte("forge-cache-game\nforge-cache-console-c1\nforge-cache-shop\n"), nil, true
		case strings.HasSuffix(line, "ls /forge-cache/mise/ws"):
			for _, a := range args {
				if strings.HasSuffix(a, ":/forge-cache") {
					listed = append(listed, strings.TrimSuffix(a, ":/forge-cache"))
				}
			}
			return []byte("not-a-workspace\n"), nil, true
		}
		return nil, nil, true
	})
	r := &Reaper{docker: docker.Scope{Client: daemon}}
	r.Cache(t.Context(), true)
	if strings.Join(listed, ",") != "forge-cache,forge-cache-game,forge-cache-shop" {
		t.Fatalf("swept %v", listed)
	}
}
