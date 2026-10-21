package worker

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The agent image is built from its context by every AppTown and worker, so
// its build must not change under them: the base is pinned by digest, each
// download is pinned by version and checked against its SHA-256 (the sha
// helper the Dockerfile installs, or sha256sum -c), and each tool installed
// by a package manager names its version.
//
// Kills: `curl … | sh`, `gopls@latest`, an unversioned `uv tool install`, a
// floating base image, and a new download added without its hash.
func TestTheAgentImageChecksEveryDownload(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../deploy/apptown-agent/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range dockerfileInstructions(string(raw)) {
		switch {
		case strings.HasPrefix(step, "FROM ") && !strings.Contains(step, "@sha256:"):
			t.Errorf("base image not pinned by digest: %s", step)
		case strings.HasPrefix(step, "RUN ") && strings.Contains(step, "curl ") && !strings.Contains(step, "apt-get install"):
			if strings.Contains(step, "| sh") || strings.Contains(step, "| bash") {
				t.Errorf("a download piped into a shell: %s", step)
			}
			if strings.Count(step, "curl ") > strings.Count(step, "sha \"")+strings.Count(step, "sha256sum -c") {
				t.Errorf("a download without its checksum: %s", step)
			}
		}
		if strings.Contains(step, "@latest") {
			t.Errorf("an unpinned version: %s", step)
		}
		if m := regexp.MustCompile(`uv tool install (\S+)`).FindStringSubmatch(step); m != nil && !strings.Contains(m[1], "==") {
			t.Errorf("an unversioned uv tool: %s", m[1])
		}
	}
}

// The agent image carries no Go toolchain: a project's steps get Go from
// the project's own mise.toml (private/design/plans/agent-image-trim-2026-10-17.md),
// and gopls is built in a stage of its own, of which only the binary is
// copied. So nothing in the final stage downloads, unpacks or runs Go.
//
// Kills: the Go tarball back in the image, `go install` in the final stage,
// and /usr/local/go on its PATH.
func TestTheAgentImageShipsNoGoToolchain(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../deploy/apptown-agent/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	steps := dockerfileInstructions(string(raw))
	last := -1
	for i, step := range steps {
		if strings.HasPrefix(step, "FROM ") {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("no FROM in the agent Dockerfile")
	}
	for _, step := range steps[last:] {
		for _, mark := range []string{"go.dev/dl", "/usr/local/go", "go install ", "forge.go.version"} {
			if strings.Contains(step, mark) {
				t.Errorf("the agent image's final stage has Go (%s): %s", mark, step)
			}
		}
	}
}

// Agents verify their work with the ordinary shell tools, so the final stage
// installs them: a missing one sends an agent looking for a workaround and
// filing a complaint (#93, `file` for a file-signature check).
//
// Kills: dropping any of these from the final stage's apt-get install.
func TestTheAgentImageCarriesTheShellToolsAgentsUse(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../deploy/apptown-agent/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	steps := dockerfileInstructions(string(raw))
	last := -1
	for i, step := range steps {
		if strings.HasPrefix(step, "FROM ") {
			last = i
		}
	}
	installed := map[string]bool{}
	for _, step := range steps[last+1:] {
		_, list, ok := strings.Cut(step, "apt-get install")
		if !ok {
			continue
		}
		list, _, _ = strings.Cut(list, "&&")
		for _, pkg := range strings.Fields(list) {
			installed[pkg] = true
		}
	}
	for _, tool := range []string{"git", "curl", "ripgrep", "fd-find", "jq", "file"} {
		if !installed[tool] {
			t.Errorf("the agent image's final stage does not install %s", tool)
		}
	}
}

// dockerfileInstructions joins continued lines and drops comments.
func dockerfileInstructions(text string) []string {
	var out []string
	cur := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cur += " " + strings.TrimSuffix(trimmed, "\\")
		if !strings.HasSuffix(trimmed, "\\") {
			out = append(out, strings.TrimSpace(cur))
			cur = ""
		}
	}
	return out
}
