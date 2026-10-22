"""Regressions for scripts/pins-check.py: shared tool versions agree with deploy/tool-pins.txt."""
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

CHECK = Path(__file__).with_name("pins-check.py")

PINS = """\
# tool    version   pinned as
node      24        image node
uv        0.13.0    arg UV_VERSION, image ghcr.io/astral-sh/uv
trivy     0.66.0    image aquasec/trivy
semgrep   1.99.0    image semgrep/semgrep
"""

DOCKERFILE = """\
FROM node:24-slim@sha256:d6aa
# a comment naming aquasec/trivy:0.1.0 is not a pin
ARG UV_VERSION=0.13.0
ARG MISE_MIRROR=https://github.com
"""

SCANNERS = """\
scanners:
  - name: trivy
    image: aquasec/trivy:0.66.0
  - name: semgrep
    image: semgrep/semgrep:1.99.0
  - name: uv-lock-check
    image: ghcr.io/astral-sh/uv:0.13.0-python3.12-alpine
  - name: npm
    image: node:24-alpine
"""

CATALOG = """\
package integration

var presets = map[string]string{"trivy": "aquasec/trivy:0.66.0", "semgrep": "semgrep/semgrep:1.99.0", "sidecar": "docker:dind"}
"""


class PinsCheckTest(unittest.TestCase):
    def setUp(self):
        self.tree = tempfile.TemporaryDirectory(prefix="forge-pins-check-")
        self.addCleanup(self.tree.cleanup)
        self.root = Path(self.tree.name)
        self.write("deploy/tool-pins.txt", PINS)
        self.write("deploy/apptown-agent/Dockerfile", DOCKERFILE)
        self.write("stock/scanners.yaml", SCANNERS)
        self.write("internal/forge/integration/catalog.go", CATALOG)

    def write(self, rel, text):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(textwrap.dedent(text))

    def run_check(self):
        return subprocess.run([sys.executable, str(CHECK), str(self.root)], capture_output=True, text=True)

    def assertFails(self, *want):
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        for w in want:
            self.assertIn(w, out.stdout)

    def test_agreeing_versions_pass(self):
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    # Kills: two versions of one tool, in two files.
    def test_a_second_version_of_a_tool_fails(self):
        self.write("internal/forge/integration/catalog.go", CATALOG.replace("aquasec/trivy:0.66.0", "aquasec/trivy:0.58.1"))
        self.assertFails("internal/forge/integration/catalog.go:3: trivy is pinned at 0.58.1")

    # Kills: a variant suffix (-alpine, -python3.12-alpine) read as the version.
    def test_the_version_is_the_tag_before_its_variant(self):
        self.write("stock/scanners.yaml", SCANNERS.replace("uv:0.13.0-python3.12-alpine", "uv:0.8.17-python3.12-alpine"))
        self.assertFails("stock/scanners.yaml:7: uv is pinned at 0.8.17")

    # Kills: a Dockerfile ARG drifting from the list.
    def test_a_dockerfile_arg_is_a_pin(self):
        self.write("deploy/apptown-agent/Dockerfile", DOCKERFILE.replace("UV_VERSION=0.13.0", "UV_VERSION=0.12.0"))
        self.assertFails("deploy/apptown-agent/Dockerfile:3: uv is pinned at 0.12.0 (UV_VERSION)")

    # Kills: a new download or scanner the list does not name.
    def test_an_unlisted_pin_in_the_dockerfile_or_scanners_fails(self):
        self.write("deploy/apptown-agent/Dockerfile", DOCKERFILE
                   + "ARG GLAB_VERSION=1.55.0\nFROM golang:1.27.0-bookworm@sha256:ded3 AS gopls\n")
        self.write("stock/scanners.yaml", SCANNERS + "  - name: hadolint\n    image: hadolint/hadolint:v2.12.0-alpine\n")
        self.assertFails("GLAB_VERSION is not in deploy/tool-pins.txt", "the image golang is not in",
                         "stock/scanners.yaml:11: the image hadolint/hadolint is not in")

    # Kills: a stale line, for a tool nothing pins any more.
    def test_a_listed_tool_pinned_nowhere_fails(self):
        self.write("deploy/tool-pins.txt", PINS + "glab  1.55.0  arg GLAB_VERSION\n")
        self.assertFails("deploy/tool-pins.txt:6: glab is not pinned as arg GLAB_VERSION")

    # Kills: test fixtures and seeds held to the list (they use arbitrary
    # versions, or belong to a project); and a tag that is not a version.
    def test_tests_testdata_and_seeds_are_not_read(self):
        self.write("internal/forge/x_test.go", 'package forge\nconst img = "node:18-alpine"\n')
        self.write("internal/forge/testdata/x.go", 'package testdata\nconst img = "node:18-alpine"\n')
        self.write("stock/seeds/app/compose.yaml", "image: node:20-slim\n")
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    # Kills: an example or history in a comment read as a pin.
    def test_a_full_line_comment_is_not_a_pin(self):
        self.write("deploy/apptown-agent/Dockerfile", DOCKERFILE)
        self.write("internal/forge/integration/catalog.go", CATALOG + "// trivy once ran as aquasec/trivy:0.58.1\n")
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    # The Docker CLI and the private daemon's image are two tools with two
    # versions (a client and a daemon); each agrees with its own line.
    def test_the_docker_cli_and_its_daemon_image_are_two_tools(self):
        self.write("deploy/tool-pins.txt", PINS + "docker-cli  27.5.1  arg DOCKER_VERSION\ndocker-dind  27  image docker\n")
        self.write("deploy/apptown-agent/Dockerfile", DOCKERFILE + "ARG DOCKER_VERSION=27.5.1\n")
        self.write("internal/forge/agentenv/workshop.go", 'package agentenv\nconst DindImage = "docker:27-dind"\n')
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.write("internal/forge/agentenv/workshop.go", 'package agentenv\nconst DindImage = "docker:28-dind"\n')
        self.assertFails("internal/forge/agentenv/workshop.go:2: docker-dind is pinned at 28")

    # Kills: a base image named by tag alone, which can be moved to other
    # contents; and an ARG default read past instead of through.
    def test_a_base_image_without_a_digest_fails(self):
        self.write("cmd/gate/Dockerfile", "FROM golang:1.26.10 AS build\nFROM scratch\nCOPY --from=build /x /x\n")
        self.write("deploy/ci/Dockerfile", "ARG GO_IMAGE=golang:1.26.10\nFROM ${GO_IMAGE} AS go\nFROM $UNSET\n")
        self.assertFails("cmd/gate/Dockerfile:1: the base image golang:1.26.10 names no digest",
                         "deploy/ci/Dockerfile:1: the base image golang:1.26.10 names no digest",
                         "deploy/ci/Dockerfile:3: FROM $UNSET has no ARG default")

    # scratch, an earlier stage, --platform and a pinned ARG default pass;
    # seeds and testdata are not AppTown's own builds.
    def test_pinned_bases_stages_and_scratch_pass(self):
        self.write("deploy/Dockerfile", "FROM --platform=$BUILDPLATFORM golang:1.26.10@sha256:9ba7 AS go\n"
                   "FROM go AS notices\nFROM scratch\n")
        self.write("deploy/ci/Dockerfile", "ARG NODE_IMAGE=node:24@sha256:6449\nFROM ${NODE_IMAGE}\n")
        self.write("stock/seeds/app/Dockerfile", "FROM node:24-slim\n")
        self.write("internal/x/testdata/app/Dockerfile", "FROM postgres:16-alpine\n")
        self.write("internal/x/testdata/sub.Dockerfile", "FROM apptown-agent:latest\n")
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

if __name__ == "__main__":
    unittest.main()
