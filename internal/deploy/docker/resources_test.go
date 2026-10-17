package docker

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
)

// Every app part's container is held to the resources its contract
// declares, the public part and the others alike, and a part that
// declares nothing is started with no limit at all.
func TestAPartsContainerIsHeldToItsDeclaredResources(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(`
app:
  web:   { image: "app:web", public: true, resources: { memory: 512M, cpu: 1.5 } }
  worker: { image: "app:worker", command: ["work"], resources: { memory: 1G } }
  cron:  { image: "app:cron", command: ["tick"] }
`))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeClient{}
	f.reply = func(req Command) ([]byte, error) {
		if len(req.Args) > 0 && req.Args[0] == "port" {
			return []byte("0.0.0.0:43210\n"), nil
		}
		return nil, nil
	}
	dk := Scope{Client: f}
	plan := appcontract.Prepare(c, nil)
	if _, _, err := bringUpParts(t.Context(), dk, "pvtest", "forge-preview-limits", t.TempDir(), "", plan, Delivery{}, Ownership{}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHost(Dependencies{}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: "forge-preview-limits", Workspace: t.TempDir()}, plan.PublicPart(), "", nil); err != nil {
		t.Fatal(err)
	}
	runs := map[string][]string{}
	for _, args := range f.args {
		if len(args) > 0 && args[0] == "run" {
			for _, a := range args {
				if strings.HasPrefix(a, "app:") {
					runs[a] = args
				}
			}
		}
	}
	for image, want := range map[string][]string{
		"app:web":    {"--memory", "536870912", "--memory-swap", "536870912", "--cpus", "1.5"},
		"app:worker": {"--memory", "1073741824", "--memory-swap", "1073741824"},
	} {
		args, ok := runs[image]
		if !ok {
			t.Fatalf("%s was never started: %v", image, f.args)
		}
		for i := 0; i < len(want); i += 2 {
			at := slices.Index(args, want[i])
			if at < 0 || at+1 >= len(args) || args[at+1] != want[i+1] {
				t.Errorf("%s: %s %s not on its run: %q", image, want[i], want[i+1], args)
			}
		}
	}
	if args := runs["app:worker"]; slices.Contains(args, "--cpus") {
		t.Errorf("the worker declares no cpu but was given one: %q", args)
	}
	cron, ok := runs["app:cron"]
	if !ok {
		t.Fatalf("cron was never started")
	}
	for _, flag := range []string{"--memory", "--memory-swap", "--cpus"} {
		if slices.Contains(cron, flag) {
			t.Errorf("cron declares no resources but was started with %s: %q", flag, cron)
		}
	}
}

// Every container a deployment or preview runs keeps a bounded log on its
// host: app parts (the public one and the others), a preview's throwaway
// needs, a standing need and an init step, each started with the json-file
// driver rotated at 10 MiB, three files kept.
//
// Kills: the rotation dropped from any one of the five run commands.
func TestEveryContainerAppKindKeepsABoundedLog(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(`
app:
  web:    { image: "app:web", public: true }
  worker: { image: "app:worker", command: ["work"] }
init:
  - { name: migrate, image: "app:migrate", command: ["migrate"] }
`))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeClient{}
	f.reply = func(req Command) ([]byte, error) {
		switch {
		case len(req.Args) > 0 && req.Args[0] == "port":
			return []byte("0.0.0.0:43210\n"), nil
		case len(req.Args) > 1 && req.Args[0] == "inspect" && !strings.HasPrefix(req.Args[1], "-"):
			return []byte("Error: No such object: " + req.Args[1]), errors.New("exit status 1")
		}
		return nil, nil
	}
	dk := Scope{Client: f}
	plan := appcontract.Prepare(c, nil)
	if _, _, err := bringUpParts(t.Context(), dk, "pvtest", "forge-preview-logs", t.TempDir(), "", plan, Delivery{}, Ownership{}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHost(Dependencies{}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: "forge-preview-logs", Workspace: t.TempDir()}, plan.PublicPart(), "", nil); err != nil {
		t.Fatal(err)
	}
	if err := runServiceContainers(t.Context(), dk, "pvtest", "net", []deploy.ServiceSpec{{Name: "cache", Image: "need:throwaway"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureStandingServices(t.Context(), dk, "envtest", []deploy.ServiceSpec{{Name: "db", Image: "need:standing"}}, "", Ownership{}); err != nil {
		t.Fatal(err)
	}
	if err := runInitContainers(t.Context(), dk, "pvtest", "net", plan, Delivery{}); err != nil {
		t.Fatal(err)
	}
	runs := map[string][]string{}
	for _, args := range f.args {
		if len(args) > 0 && args[0] == "run" {
			for _, a := range args {
				if strings.HasPrefix(a, "app:") || strings.HasPrefix(a, "need:") {
					runs[a] = args
				}
			}
		}
	}
	want := []string{"--log-driver", "json-file", "--log-opt", "max-size=10m", "--log-opt", "max-file=3"}
	for _, image := range []string{"app:web", "app:worker", "need:throwaway", "need:standing", "app:migrate"} {
		args, ok := runs[image]
		if !ok {
			t.Errorf("%s was never started: %v", image, f.args)
			continue
		}
		before := args[:slices.Index(args, image)] // the image's own arguments are not docker's
		for i := 0; i < len(want); i += 2 {
			found := false
			for at := range before[:len(before)-1] {
				found = found || (before[at] == want[i] && before[at+1] == want[i+1])
			}
			if !found {
				t.Errorf("%s: %s %s not on its run: %q", image, want[i], want[i+1], args)
			}
		}
	}
}
