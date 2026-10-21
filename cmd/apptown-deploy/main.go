// apptown-deploy runs a described application on a Docker host with nothing
// but the deploy library: the proof that internal/deploy stands alone. No
// database and no AppTown package (the shape checker asserts it): a tree with
// a .apptown/app.yaml, a Docker daemon, and what the host said.
//
//	apptown-deploy -app game -tree /path/to/checkout                 # run it as generation 1
//	apptown-deploy -app game -tree ... -generation 2 -port 41233     # the next generation, same port
//	apptown-deploy -app game -observe -generation 2                  # what the host says now
//	apptown-deploy -app game -teardown -generation 2                 # remove the container
//
// Secrets: APPTOWN_DEPLOY_SECRETS names environment variables handed to every
// consumer. An app that declares needs is refused by name: this tool
// fulfils none. An attached host is -host ssh://... with -hostname and -domain.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
)

func main() {
	var (
		slug       = flag.String("app", "", "the application's slug, for naming on the host")
		name       = flag.String("deployment", "main", "which standing deployment of it")
		tree       = flag.String("tree", "", "a staged, self-contained checkout to run")
		id         = flag.String("id", "1", "the placement's name on the host (its labels)")
		prefix     = flag.String("prefix", string(docker.Current), "what its host names begin with (forge- for a deployment made before 1.0)")
		generation = flag.Int64("generation", 1, "the generation to run, observe or tear down")
		port       = flag.Int("port", 0, "pin the published port; 0 lets the host pick")
		host       = flag.String("host", "", "an attached host's DOCKER_HOST (ssh://...); empty is the local daemon")
		hostname   = flag.String("hostname", "", "the attached host's address, for its URL")
		domain     = flag.String("domain", "", "the attached host's ingress domain, when it has one")
		secrets    = flag.String("secrets", os.Getenv("APPTOWN_DEPLOY_SECRETS"), "comma-separated environment variables to hand every consumer (APPTOWN_DEPLOY_SECRETS)")
		observe    = flag.Bool("observe", false, "say what the host reports for the placement")
		teardown   = flag.Bool("teardown", false, "remove the placement's container for the given generation")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Experimental: not promised in 1.x; it exists to show the deploy library stands alone (stock/docs/guides/compatibility.md).")
		fmt.Fprintln(os.Stderr, "usage of apptown-deploy:")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *slug == "" || (!*observe && !*teardown && *tree == "") {
		fmt.Fprintln(os.Stderr, "apptown-deploy needs -app, and -tree unless -observe or -teardown")
		os.Exit(2)
	}
	scope := docker.Scope{Client: docker.CLI{}}
	if *host != "" {
		scope = scope.WithHost(*host, first(*hostname, *host), *domain)
	}
	env := envSecrets(strings.Split(*secrets, ","))
	driver := &docker.Driver{
		Scope:        func(context.Context) (docker.Scope, error) { return scope, nil },
		Boot:         docker.NewHost(docker.Dependencies{Secrets: env, Needs: noNeeds{}, Probe: &docker.Probe{}}),
		URL:          func(_, _ string, port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) },
		AfterRelease: func(deploy.Placement) {},
	}
	ctx := context.Background()
	names, err := docker.ParsePrefix(*prefix)
	if err != nil {
		fail("%v", err)
	}
	container := names.Container(*slug, *name)
	place := deploy.Placement{Label: deploy.HostLabel(*id), Name: *name, HostPort: *port, Container: container, Generation: *generation, HostPrefix: string(names)}
	logf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }

	switch {
	case *observe:
		o, err := driver.Observe(ctx, place)
		if err != nil {
			fail("%v", err)
		}
		report(o)
	case *teardown:
		if _, err := driver.Teardown(ctx, place, *generation, deploy.TakeDown{}); err != nil {
			fail("%v", err)
		}
		fmt.Printf("removed %s\n", container)
	default:
		if _, err := appcontract.Parse(mustRead(*tree + "/.apptown/app.yaml")); err != nil {
			fail("the contract in %s: %v", *tree, err)
		}
		revision, err := (deploy.SelectedSource{Repository: *tree, Revision: "HEAD"}).Resolve(ctx)
		if err != nil {
			revision = "working-tree" // a staged tree need not be a repository
		}
		art, err := docker.SourceAt(*tree, revision, *slug, nil)
		if err != nil {
			fail("preparing the source: %v", err)
		}
		place.Container, place.Generation = "", *generation-1
		key := fmt.Sprintf("%s-%s-%d", *slug, *name, *generation)
		o, err := driver.Reconcile(ctx, place, *generation, art, key, logf, func(noted map[string]any) error {
			keys := make([]string, 0, len(noted))
			for k := range noted {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Printf("noted: %s\n", strings.Join(keys, ", "))
			return nil
		})
		if err != nil {
			fail("%v", err)
		}
		report(o)
	}
}

func report(o deploy.Observation) {
	fmt.Printf("generation %d: %s, %s", o.Generation, o.Rollout, o.Health)
	if o.URL != "" {
		fmt.Printf(", serving at %s", o.URL)
	}
	if o.ImageDigest != "" {
		fmt.Printf(" (image %s)", o.ImageDigest)
	}
	fmt.Println()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "apptown-deploy: "+format+"\n", args...)
	os.Exit(1)
}

func mustRead(path string) []byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		fail("reading %s: %v", path, err)
	}
	return raw
}

func first(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// envSecrets hands every consumer the same named environment variables;
// AppTown's broker does better, this tool does not pretend to.
type envSecrets []string

func (e envSecrets) EnvFor(string) map[string]string {
	out := map[string]string{}
	for _, k := range e {
		if k = strings.TrimSpace(k); k != "" {
			if v, ok := os.LookupEnv(k); ok {
				out[k] = v
			}
		}
	}
	return out
}

func (e envSecrets) Resolve(context.Context, string, string, string, *appcontract.Contract) (docker.Delivery, error) {
	env := e.EnvFor("")
	names := make([]docker.SecretReceipt, 0, len(env))
	for k := range env {
		names = append(names, docker.SecretReceipt{Name: k, Store: "environment"})
	}
	return docker.Delivery{Env: func(string) map[string]string { return env }, Receipts: names, Profile: "environment"}, nil
}

// noNeeds fulfils nothing: an app that declares needs is refused by name.
type noNeeds struct{}

func (noNeeds) Fulfil(ctx context.Context, needs []string, _ map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	if len(needs) == 0 {
		return nil, nil, nil
	}
	return nil, nil, fmt.Errorf("this app needs %s, which apptown-deploy cannot fulfil: it runs no dependency catalogue", strings.Join(needs, ", "))
}
func (noNeeds) Data(context.Context, string) (string, error) { return "", nil }
