package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const appYamlPath = ".forge/app.yaml"

func readLocalAppYaml() (string, error) {
	b, err := os.ReadFile(appYamlPath)
	if err != nil {
		return "", fmt.Errorf("no %s here — run from the repo root of an AppTown project", appYamlPath)
	}
	return string(b), nil
}

// forge config schema|validate
func cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: forge config schema | forge config validate [path]")
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

// forge dev — local development in two gears (design/decisions/forge-native-contract.md):
//
//	forge dev        process-mode: bring up the app's NEEDS (emulators) and write
//	                 .env; you run the app yourself (`source .env && npm run dev`).
//	forge dev up     container-mode: render + run the WHOLE app via compose.
//	forge dev down   tear either down.
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
		raw, err := api.request("POST", "/dev-compose", map[string]string{"appYaml": yaml, "mode": mode})
		if err != nil {
			return err
		}
		var out struct {
			Compose  string            `json:"compose"`
			Env      map[string]string `json:"env"`
			Procfile string            `json:"procfile"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		// Generated files live under .forge/, which the forge never stages
		// and every step ignores — so running `forge dev` in a change's
		// workspace leaves no trace in the tree and touches no .gitignore
		// (an agent's README fix once carried a .gitignore line for this).
		if err := os.MkdirAll(".forge", 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(devComposePath, []byte(out.Compose), 0o644); err != nil {
			return err
		}

		if mode == "needs" {
			// Write the connection env for the host process to source.
			var b strings.Builder
			for k, v := range out.Env {
				b.WriteString(k + "=" + v + "\n")
			}
			_ = os.WriteFile(devEnvPath, []byte(b.String()), 0o600)
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
			fmt.Printf("wrote %s (generated; under .forge/, never committed)\n", devComposePath)
			return nil
		}
		fmt.Println("$ docker compose up -d --build")
		if err := runDocker("up", "-d", "--build"); err != nil {
			return err
		}
		if host := publishedPortsHost(os.Getenv("DOCKER_HOST")); host != "" {
			fmt.Printf("the app's published ports answer on %[1]s, this session's Docker host, not on localhost (curl http://%[1]s:<port>/)\n", host)
		}
		return nil
	case "down":
		return runDocker("down", "-v")
	}
	return fmt.Errorf("usage: forge dev [ | up | generate | down]")
}

// Generated local-dev files live under .forge/: the forge never stages that
// directory and steps ignore it, so `forge dev` leaves no trace in the tree.
const (
	devComposePath  = ".forge/docker-compose.yml"
	devEnvPath      = ".forge/dev.env"
	devProcfilePath = ".forge/Procfile"
)

// devComposeArgs runs compose on the generated file from the repo root: the
// file sits under .forge/, but its relative paths (build contexts, static
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

func runDocker(dcArgs ...string) error {
	// Say what is missing before trying: a docker client without the compose
	// plugin parses "compose up -d" as its own flags and fails confusingly
	// (complaint #54). This is a runtime capability gap, not a contract error.
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		return fmt.Errorf("this machine's docker client has no compose plugin, which forge dev up needs; this is a platform problem, not a problem with your contract (report it with forge complain)")
	}
	cmd := exec.Command("docker", devComposeArgs(dcArgs...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %v: %w (is docker running locally?)", dcArgs, err)
	}
	return nil
}
