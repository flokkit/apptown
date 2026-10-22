package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

// carryDaemon is a daemon whose standing postgres service was made before
// needs had named volumes: its data is mounted from mounts (as docker
// inspect's .Mounts lines print it, "" for none), its environment is env and
// its memory limit is memory bytes.
func carryDaemon(mounts, env string, memory string, mountsErr error) *fakeClient {
	return &fakeClient{reply: func(req Command) ([]byte, error) {
		args := strings.Join(req.Args, " ")
		switch {
		case strings.HasPrefix(args, "inspect") && strings.Contains(args, ".Mounts"):
			return []byte(mounts), mountsErr
		case strings.HasPrefix(args, "inspect") && strings.Contains(args, "Config.Env"):
			return []byte(env), nil
		case strings.HasPrefix(args, "inspect") && strings.Contains(args, "Config.Image"):
			return []byte("postgres:16-alpine\n"), nil
		case strings.HasPrefix(args, "inspect") && strings.Contains(args, "HostConfig.Memory"):
			return []byte(memory + " 0\n"), nil
		case strings.HasPrefix(args, "volume inspect"):
			// No named volume yet: these services predate them.
			return []byte("Error: No such volume: " + req.Args[len(req.Args)-1]), errors.New("exit status 1")
		}
		return nil, nil
	}}
}

const (
	carryPath = "/var/lib/postgresql/data"
	oldEnv    = "POSTGRES_USER=app\nPOSTGRES_DB=app\nPOSTGRES_PASSWORD=old\n"
	anonymous = "f0e1d2c3b4a5f0e1d2c3b4a5f0e1d2c3b4a5f0e1d2c3b4a5f0e1d2c3b4a5f0e1"
)

func carryService(configAtStart bool) deploy.ServiceSpec {
	return deploy.ServiceSpec{Name: "postgres", Image: "postgres:16-alpine", ConfigAtStart: configAtStart,
		Env: map[string]string{"POSTGRES_USER": "app", "POSTGRES_DB": "app", "POSTGRES_PASSWORD": "new"}}
}

func runMount(calls [][]string) string {
	for _, args := range calls {
		if len(args) > 1 && args[0] == "run" && args[1] == "-d" {
			for i, a := range args {
				if a == "-v" && i+1 < len(args) && strings.HasSuffix(args[i+1], ":"+carryPath) {
					return strings.TrimSuffix(args[i+1], ":"+carryPath)
				}
			}
			return "(none)"
		}
	}
	return "(no run)"
}

// A need service made again is made on the storage its data is in: a
// service made before needs had named volumes keeps its data in its image's
// anonymous volume (or a bind mount), and the service made again mounts
// that, never a new empty named volume. Both ways it is made again: its
// declared parameters moved (configuration it sets at every start), and a
// limit taken away.
func TestANeedMadeAgainKeepsTheStorageItsDataIsIn(t *testing.T) {
	t.Parallel()
	own := Ownership{Needs: map[string]string{"postgres": carryPath}}
	for _, c := range []struct {
		name, mounts, env, memory, want string
		configAtStart                   bool
	}{
		{"its parameters moved, data in an anonymous volume", "volume\t" + anonymous + "\t/var/lib/docker/volumes/x/_data\t" + carryPath + "\n", oldEnv, "268435456", anonymous, true},
		{"its parameters moved, data in a bind mount", "bind\t\t/srv/pg\t" + carryPath + "\n", oldEnv, "268435456", "/srv/pg", true},
		{"a limit taken away, data in an anonymous volume", "volume\t" + anonymous + "\t/var/lib/docker/volumes/x/_data\t" + carryPath + "\n", "POSTGRES_USER=app\nPOSTGRES_DB=app\nPOSTGRES_PASSWORD=new\n", "268435456", anonymous, true},
		{"already on its named volume", "volume\t" + Current.NeedVolume("env-shop-main", "postgres") + "\t/x\t" + carryPath + "\n", oldEnv, "268435456", Current.NeedVolume("env-shop-main", "postgres"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			client := carryDaemon(c.mounts, c.env, c.memory, nil)
			if _, err := EnsureStandingServices(context.Background(), Scope{Client: client}, "env-shop-main", []deploy.ServiceSpec{carryService(c.configAtStart)}, "production", own); err != nil {
				t.Fatal(err)
			}
			if got := runMount(client.args); got != c.want {
				t.Fatalf("made again on %q, want %q: %v", got, c.want, client.args)
			}
			if c.want != Current.NeedVolume("env-shop-main", "postgres") && hasStandingCall(client.args, "volume create") {
				t.Fatalf("an empty named volume was made beside the data: %v", client.args)
			}
		})
	}
}

// An existing need service with nothing mounted where its data belongs may
// hold its data in the container itself: it is refused before anything
// stops it, both ways it would be made again, and so is one whose mounts
// could not be read.
func TestANeedWithNoStorageForItsDataIsNotMadeAgain(t *testing.T) {
	t.Parallel()
	own := Ownership{Needs: map[string]string{"postgres": carryPath}}
	for _, c := range []struct {
		name, env, want string
		mountsErr       error
	}{
		{"its parameters moved", oldEnv, "mounts nothing", nil},
		{"a limit taken away", "POSTGRES_USER=app\nPOSTGRES_DB=app\nPOSTGRES_PASSWORD=new\n", "mounts nothing", nil},
		{"its mounts could not be read", oldEnv, "reading where it keeps its data", errors.New("daemon gone")},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			client := carryDaemon("", c.env, "268435456", c.mountsErr)
			_, err := EnsureStandingServices(context.Background(), Scope{Client: client}, "env-shop-main", []deploy.ServiceSpec{carryService(true)}, "production", own)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("not refused: %v", err)
			}
			for _, verb := range []string{"stop", "rm", "run -d"} {
				if hasStandingCall(client.args, verb) {
					t.Fatalf("it was touched (%s): %v", verb, client.args)
				}
			}
		})
	}
}

// A deliberate reset (synthetic data, parameters a store keeps in its data
// moved) still lets its data go: the service is made on its named volume,
// not on the anonymous one it used.
func TestASyntheticResetStillStartsOnNewStorage(t *testing.T) {
	t.Parallel()
	own := Ownership{Needs: map[string]string{"postgres": carryPath}}
	client := carryDaemon("volume\t"+anonymous+"\t/x\t"+carryPath+"\n", oldEnv, "268435456", nil)
	if _, err := EnsureStandingServices(context.Background(), Scope{Client: client}, "env-shop-main", []deploy.ServiceSpec{carryService(false)}, "synthetic", own); err != nil {
		t.Fatal(err)
	}
	if got := runMount(client.args); got != Current.NeedVolume("env-shop-main", "postgres") {
		t.Fatalf("a reset kept the old storage: %q", got)
	}
}
