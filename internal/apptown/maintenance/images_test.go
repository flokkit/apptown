package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/stock"
)

func TestImageSweepRequiresAValidExpiredAge(t *testing.T) {
	t.Parallel()
	daemon := newDockerFake()
	now := time.Now()
	rows := []string{
		"apptown-conform:short\t?",
		"apptown-conform:missing",
		"apptown-conform:recent\t" + now.Add(-20*time.Hour).In(time.FixedZone("PST", -8*60*60)).Format("2006-01-02 15:04:05 -0700 MST"),
		"apptown-conform:old\t" + now.Add(-48*time.Hour).UTC().Format("2006-01-02 15:04:05 -0700 MST"),
		// Made two days ago, rebuilt from cache an hour ago: the rebuild
		// keeps the old creation time, and the tag says it is new.
		"apptown-conform:rebuilt\t" + now.Add(-48*time.Hour).UTC().Format("2006-01-02 15:04:05 -0700 MST"),
		"apptown-conform:retagged-long-ago\t" + now.Add(-72*time.Hour).UTC().Format("2006-01-02 15:04:05 -0700 MST"),
	}
	tagged := map[string]time.Time{"apptown-conform:rebuilt": now.Add(-time.Hour), "apptown-conform:retagged-long-ago": now.Add(-48 * time.Hour)}
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "image" && args[1] == "ls" {
			return []byte(strings.Join(rows, "\n")), nil, true
		}
		if len(args) > 1 && args[0] == "image" && args[1] == "inspect" {
			// Docker's zero time for an image it never tagged here.
			at, _ := json.Marshal(tagged[args[len(args)-1]])
			return append(at, '\n'), nil, true
		}
		return nil, nil, false
	})
	a := &Reaper{docker: docker.Scope{Client: daemon}}
	got := a.Images(context.Background(), false)
	slices.Sort(got)
	if want := []string{"apptown-conform:old", "apptown-conform:retagged-long-ago"}; !slices.Equal(got, want) {
		t.Fatalf("age selection: %v, want %v", got, want)
	}
	if removed := daemon.Matching("rmi "); len(removed) != 2 {
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

// On an AppTown with no local Docker, the reapers of its containers, images
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
// AppTown that cannot read its records keeps every image. A change's are
// Preview's, whatever its record says.
func TestImageSweepReapsImagesWhoseOwnerNoLongerExists(t *testing.T) {
	t.Parallel()
	for _, readable := range []bool{true, false} {
		daemon := newDockerFake()
		daemon.SetReply(func(args []string) ([]byte, error, bool) {
			if len(args) > 1 && args[0] == "image" && args[1] == "ls" {
				return []byte("apptown-preview-build-apptown-preview-run-00000000-002a-7000-8000-000000000000:latest\napptown-preview-build-apptown-preview-change-00000000-0009-7000-8000-000000000000-web:latest"), nil, true
			}
			return nil, nil, false
		})
		db := databaseSuite.New(t)
		if !readable {
			_ = db.Close()
		}
		r := &Reaper{db: db, docker: docker.Scope{Client: daemon}}
		got := r.Images(context.Background(), false)
		if readable && (len(got) != 1 || got[0] != "apptown-preview-build-apptown-preview-run-00000000-002a-7000-8000-000000000000:latest") || !readable && len(got) != 0 {
			t.Fatalf("readable=%v: reaped %v", readable, got)
		}
	}
}

// A container's age is judged on AppTown's clock, which stamped its
// apptown.created label: a step's container AppTown labelled ahead of this
// machine's clock is reaped once AppTown's clock is past the idle limit,
// whatever the machine's says. (Its step, 424242, does not exist: finished.)
// A preview of the same age is not the age reaper's: the last look at it
// decides (preview.Service.Tend).
func TestAContainersAgeIsJudgedOnAppTownsClock(t *testing.T) {
	t.Parallel()
	apptownNow := time.Now().Add(72 * time.Hour).UTC()
	created := apptownNow.Add(-3 * time.Hour)
	daemon := newDockerFake()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "ps" {
			return []byte("apptown-step-00000006-7932-7000-8000-000000000000\tstep\t" + created.Format(time.RFC3339) + "\t00000006-7932-7000-8000-000000000000\tc1\n" +
				"apptown-preview-p\tpreview\t" + created.Format(time.RFC3339) + "\tapptown-preview-p\tc2"), nil, true
		}
		return nil, nil, false
	})
	sessions := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: t.TempDir()}})
	r := New(databaseSuite.New(t), Dependencies{ToolVersions: stock.Versions(), Docker: docker.Scope{Client: daemon}, Sessions: sessions, Now: func() time.Time { return apptownNow }}, t.TempDir())
	if got := r.Containers(context.Background(), true); len(got) != 1 || got[0] != "apptown-step-00000006-7932-7000-8000-000000000000" {
		t.Fatalf("would reap %v, want the step's container three hours old on AppTown's clock, and no preview", got)
	}
}

// An image a container still uses stays, and the sweep says nothing about
// it: it is tried again each sweep, and named only once it goes.
// Not parallel: it reads the process's log.
func TestAnImageInUseIsKeptQuietly(t *testing.T) {
	daemon := newDockerFake()
	old := time.Now().Add(-48 * time.Hour).UTC().Format("2006-01-02 15:04:05 -0700 MST")
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		switch {
		case len(args) > 1 && args[0] == "image" && args[1] == "ls":
			return []byte("apptown-conform:used\t" + old), nil, true
		case args[0] == "rmi":
			return []byte("Error response from daemon: conflict: unable to remove repository reference \"apptown-conform:used\" (must force) - container 0123abcd is using its referenced image 4567ef"), errors.New("exit status 1"), true
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
// Each listing is read from a file in its container; a volume whose
// listing did not come back has nothing removed.
//
// Kills: a sweep of forge-cache alone, which never reaped what steps now
// write to their project's volume.
func TestTheCacheSweepVisitsEveryProjectsCache(t *testing.T) {
	t.Parallel()
	daemon := newDockerFake()
	var listed []string
	daemon.SetResults(func(c dockerfake.ResultCommand) (dockerfake.ResultAnswer, bool) {
		if c.Script != cacheListing {
			return dockerfake.ResultAnswer{}, false
		}
		for _, a := range c.Args {
			if strings.HasSuffix(a, ":/apptown-cache") {
				listed = append(listed, strings.TrimSuffix(a, ":/apptown-cache"))
			}
		}
		return dockerfake.ResultAnswer{}, true // no listing came back
	})
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "volume ls") && strings.Contains(line, "name=apptown-project-cache-"):
			return []byte("apptown-project-cache-00000000-0001-7000-8000-000000000000\napptown-project-cache-unknown\n"), nil, true
		case strings.HasPrefix(line, "volume ls") && strings.Contains(line, "name=apptown-cache-"):
			return []byte("apptown-cache-game\napptown-cache-console-c1\napptown-cache-shop\n"), nil, true
		case strings.Contains(line, "rm -rf"):
			t.Errorf("removed with no listing: %s", line)
		}
		return nil, nil, true
	})
	r := &Reaper{docker: docker.Scope{Client: daemon}, tools: stock.Versions()}
	if reaped := r.Cache(t.Context(), false); len(reaped) != 0 {
		t.Fatalf("reaped %v with no listing", reaped)
	}
	if strings.Join(listed, ",") != "apptown-cache,apptown-cache-game,apptown-cache-shop,apptown-project-cache-00000000-0001-7000-8000-000000000000" {
		t.Fatalf("swept %v", listed)
	}
}

// A cache sweep the daemon's shutdown stops mid-volume removes the
// container it made for that volume and asks Docker nothing about the
// volumes after it: the shutdown waits on this sweep, and a removal that
// outlives the stop, spent once per remaining volume, held it past its
// limit on a machine with hundreds of cache volumes.
//
// Kills: a read that registers its removal before checking for the stop.
func TestACacheSweepStoppedMidwayAsksDockerNothingMore(t *testing.T) {
	t.Parallel()
	daemon := newDockerFake()
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	daemon.SetResults(func(c dockerfake.ResultCommand) (dockerfake.ResultAnswer, bool) {
		stop() // the shutdown arrives while the first volume is read
		return dockerfake.ResultAnswer{Answer: []byte{}}, true
	})
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if strings.HasPrefix(strings.Join(args, " "), "volume ls") {
			return []byte("apptown-cache-game\napptown-cache-shop\napptown-cache-blog\n"), nil, true
		}
		return nil, nil, true
	})
	r := &Reaper{docker: docker.Scope{Client: daemon}, tools: stock.Versions()}
	r.Cache(ctx, false)
	made, removed := daemon.Matching("create "), daemon.Matching("rm ")
	if len(made) != 1 || len(removed) != 1 || removed[0][len(removed[0])-1] != made[0][slices.Index(made[0], "--name")+1] {
		t.Fatalf("made %v, removed %v; want the one read in flight removed and nothing else asked", made, removed)
	}
}

func TestUnavailableProjectCacheDiscoveryStartsNoSweep(t *testing.T) {
	t.Parallel()
	d := newDockerFake()
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "volume" && args[1] == "ls" {
			return nil, errors.New("daemon unavailable"), true
		}
		t.Errorf("sweep acted after unavailable inventory: %v", args)
		return nil, nil, true
	})
	r := &Reaper{docker: docker.Scope{Client: d}, tools: stock.Versions()}
	if got := r.Cache(t.Context(), false); len(got) != 0 {
		t.Fatalf("unavailable sweep claimed %v", got)
	}
}
