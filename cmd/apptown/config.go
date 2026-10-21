package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"flokkit.io/forge/internal/deploy/docker"
)

const appYamlPath = ".apptown/app.yaml"

func readLocalAppYaml() (string, error) {
	b, err := os.ReadFile(appYamlPath)
	if err != nil {
		return "", fmt.Errorf("no %s here — run from the repo root of an AppTown project", appYamlPath)
	}
	return string(b), nil
}

// apptown config schema|validate
func cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: apptown config schema | apptown config validate [path]")
	}
	switch args[0] {
	case "schema":
		raw, err := api.request("GET", "/config/schema", nil)
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	case "validate":
		path := appYamlPath
		if len(args) > 1 {
			path = args[1]
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		body := map[string]string{"appYaml": string(b)}
		if ss, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(path)), "secretspec.toml")); err == nil {
			body["secretSpec"] = string(ss)
		}
		raw, err := api.request("POST", "/config/validate", body)
		if err != nil {
			return err
		}
		var out struct {
			OK       bool     `json:"ok"`
			Errors   []string `json:"errors"`
			Warnings []string `json:"warnings"`
			Summary  struct {
				Parts []string `json:"parts"`
				Needs []string `json:"needs"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		for _, e := range out.Errors {
			fmt.Printf("  ✗ %s\n", e)
		}
		for _, wn := range out.Warnings {
			fmt.Printf("  ⚠ %s\n", wn)
		}
		if out.OK {
			fmt.Printf("✓ valid — parts: %v · needs: %v\n", out.Summary.Parts, out.Summary.Needs)
			return nil
		}
		return fmt.Errorf("%s is not valid", path)
	}
	return fmt.Errorf("unknown config subcommand %q", args[0])
}

// apptown dev — local development in two gears (design/decisions/forge-native-contract.md):
//
//	apptown dev        process-mode: bring up the app's NEEDS (emulators) and write
//	                 .env; you run the app yourself (`source .env && npm run dev`).
//	apptown dev up     container-mode: render + run the WHOLE app via compose.
//	apptown dev down   tear either down.
func cmdDev(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "", "needs", "up", "generate":
		yaml, err := readLocalAppYaml()
		if err != nil {
			return err
		}
		mode := "needs"
		if sub == "up" || sub == "generate" {
			mode = "full"
		}
		// The needs' ports answer on the Docker daemon's host: a chat with its
		// own daemon (DOCKER_HOST=tcp://dind:2375) reaches them there, not on
		// localhost (complaint #94).
		host := publishedPortsHost(os.Getenv("DOCKER_HOST"))
		raw, err := api.request("POST", "/dev-compose", map[string]string{"appYaml": yaml, "mode": mode, "host": host})
		if err != nil {
			return err
		}
		var out struct {
			Compose      string            `json:"compose"`
			Env          map[string]string `json:"env"`
			Procfile     string            `json:"procfile"`
			StaticBuilds []devStaticBuild  `json:"staticBuilds"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		// Generated files live under .apptown/, which AppTown never stages
		// and every step ignores — so running `apptown dev` in a change's
		// workspace leaves no trace in the tree and touches no .gitignore
		// (an agent's README fix once carried a .gitignore line for this).
		if err := os.MkdirAll(".apptown", 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(devComposePath, []byte(out.Compose), 0o644); err != nil {
			return err
		}

		if mode == "needs" {
			// Write the connection env for the host process to source.
			_ = os.WriteFile(devEnvPath, []byte(devEnvFile(out.Env)), 0o600)
			// A Procfile for a multi-process app (run with hivemind).
			if out.Procfile != "" {
				_ = os.WriteFile(devProcfilePath, []byte(out.Procfile), 0o644)
			}
			if err := runDocker("up", "-d"); err != nil {
				return err
			}
			fmt.Printf("needs are up (see %s). Connection env written to %s.\n", devComposePath, devEnvPath)
			if out.Procfile != "" {
				fmt.Printf("this app has multiple parts — a Procfile was written; run:  set -a && . ./%s && set +a && hivemind -f %s\n", devEnvPath, devProcfilePath)
			} else {
				fmt.Printf("run your app against them, e.g.:  set -a && . ./%s && set +a && <your dev command>\n", devEnvPath)
			}
			return nil
		}
		if sub == "generate" {
			fmt.Printf("wrote %s (generated; under .apptown/, never committed)\n", devComposePath)
			for _, b := range out.StaticBuilds {
				fmt.Printf("apptown dev up first builds the static site %s into %s\n", b.Part, b.Folder)
			}
			return nil
		}
		if err := devUpStart(out.StaticBuilds, dockerCLI); err != nil {
			return err
		}
		if host := publishedPortsHost(os.Getenv("DOCKER_HOST")); host != "" {
			fmt.Printf("the app's published ports answer on %[1]s, this session's Docker host, not on localhost (curl http://%[1]s:<port>/)\n", host)
		}
		return nil
	case "down":
		return runDocker("down", "-v")
	}
	return fmt.Errorf("usage: apptown dev [ | up | generate | down]")
}

// devStaticBuild is a static site's build that `apptown dev up` runs before
// compose, as AppTown's dev-compose answer lists it.
type devStaticBuild struct {
	Part    string `json:"part"`
	Context string `json:"context"`
	Target  string `json:"target"`
	Output  string `json:"output"`
	Folder  string `json:"folder"`
}

// buildStaticSites runs each static site's build the way a deploy does
// (docker build --output type=local), into its folder under .apptown/,
// emptied first so a file the build stopped writing is not served. It
// refuses when a build fails or writes no output folder: compose would
// otherwise serve an empty site.
func buildStaticSites(builds []devStaticBuild, build func(args ...string) error) error {
	for _, b := range builds {
		if err := os.RemoveAll(b.Folder); err != nil {
			return err
		}
		args := []string{"build"}
		if b.Target != "" {
			args = append(args, "--target", b.Target)
		}
		args = append(args, "--output", "type=local,dest="+b.Folder, b.Context)
		fmt.Printf("$ docker %s\n", strings.Join(args, " "))
		if err := build(args...); err != nil {
			return fmt.Errorf("building the static site %s: %w", b.Part, err)
		}
		// The deploy path's own rule: the output is a folder of plain
		// folders and files, holding at least one file, with no link on
		// the way to it or inside it, since the folder is then mounted
		// into the file server's container.
		if _, err := docker.ConfineStaticExport(b.Folder, b.Output); err != nil {
			return fmt.Errorf("the static site %s: %w", b.Part, err)
		}
	}
	return nil
}

// staticParts names the parts a fresh build replaced: compose keeps a running
// container whose definition did not change, and its bind mount would still
// hold the folder the build just emptied and rewrote.
func staticParts(builds []devStaticBuild) []string {
	var out []string
	for _, b := range builds {
		out = append(out, b.Part)
	}
	return out
}

// devUpStart is `apptown dev up` after the files are written: each static
// site built and checked, then compose up, then the static sites' services
// recreated so their mounts hold the folders just rebuilt. A build that
// fails or is refused stops it before compose runs.
func devUpStart(builds []devStaticBuild, docker func(quiet bool, args ...string) error) error {
	build := func(args ...string) error { return docker(false, args...) }
	if err := buildStaticSites(builds, build); err != nil {
		return err
	}
	compose := func(dcArgs ...string) error { return composeWith(docker, dcArgs...) }
	fmt.Println("$ docker compose up -d --build")
	if err := compose("up", "-d", "--build"); err != nil {
		return err
	}
	if parts := staticParts(builds); len(parts) > 0 {
		fmt.Printf("$ docker compose up -d --no-deps --force-recreate %s\n", strings.Join(parts, " "))
		return compose(append([]string{"up", "-d", "--no-deps", "--force-recreate"}, parts...)...)
	}
	return nil
}

// dockerCLI runs the person's own docker client on their terminal with
// their whole environment (their Buildx builder included), quietly for a
// probe. It is apptown dev's one place that starts a process.
func dockerCLI(quiet bool, args ...string) error {
	cmd := exec.Command("docker", args...)
	if !quiet {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	}
	return cmd.Run()
}

// Generated local-dev files live under .apptown/: AppTown never stages that
// directory and steps ignore it, so `apptown dev` leaves no trace in the tree.
const (
	devComposePath  = ".apptown/docker-compose.yml"
	devEnvPath      = ".apptown/dev.env"
	devProcfilePath = ".apptown/Procfile"
)

// devComposeArgs runs compose on the generated file from the repo root: the
// file sits under .apptown/, but its relative paths (build contexts, static
// mounts) name the app's own tree, and compose resolves them against the
// project directory, which defaults to the file's own folder.
func devComposeArgs(dcArgs ...string) []string {
	return append([]string{"compose", "--project-directory", ".", "-f", devComposePath}, dcArgs...)
}

// publishedPortsHost is where compose's published ports answer when Docker
// is another machine (DOCKER_HOST=tcp://dind:2375 in a session with its own
// daemon): that host, never localhost. "" when Docker is this machine's.
func publishedPortsHost(dockerHost string) string {
	rest, ok := strings.CutPrefix(dockerHost, "tcp://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	if h, _, found := strings.Cut(host, ":"); found && !strings.HasPrefix(host, "[") {
		host = h
	}
	return host
}

func runDocker(dcArgs ...string) error { return composeWith(dockerCLI, dcArgs...) }

// composeWith runs docker compose on the generated file through docker.
// It says what is missing before trying: a docker client without the
// compose plugin parses "compose up -d" as its own flags and fails
// confusingly (complaint #54). That is a runtime capability gap, not a
// contract error.
func composeWith(docker func(quiet bool, args ...string) error, dcArgs ...string) error {
	if err := docker(true, "compose", "version"); err != nil {
		return fmt.Errorf("this machine's docker client has no compose plugin, which apptown dev up needs; this is a platform problem, not a problem with your contract (report it with apptown complain)")
	}
	if err := docker(false, devComposeArgs(dcArgs...)...); err != nil {
		return fmt.Errorf("docker compose %v: %w (is docker running locally?)", dcArgs, err)
	}
	return nil
}

// devEnvFile is env as lines a POSIX shell sources, in name order. Each
// value is single-quoted (a ' written as '\”), so a wired value with
// spaces, $ or quotes (host=… dbname=app) arrives as written.
func devEnvFile(env map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		b.WriteString(k + "='" + strings.ReplaceAll(env[k], "'", `'\''`) + "'\n")
	}
	return b.String()
}
