"""homebrew-formula.py writes each platform's archive URL with that archive's
own sha256, for every platform the release archives are built for, and
refuses SHA256SUMS that would leave a hash wrong or missing."""
import importlib.util
import os
import re
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("homebrew_formula", os.path.join(HERE, "homebrew-formula.py"))
hf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hf)

with open(hf.TEMPLATE) as f:
    TEMPLATE = f.read()

V = "1.2.3"


def digest(p):
    # A distinct hash per platform, so a swapped pair shows.
    return (p.encode().hex() * 8)[:64]


def sums(platforms=hf.PLATFORMS, version=V, extra=""):
    return "".join(f"{digest(p)}  apptown-{version}-{p}.tar.gz\n" for p in platforms) + extra


class TheFormula(unittest.TestCase):
    def test_each_platform_url_carries_its_own_archives_hash(self):
        out = hf.formula(V, sums(), TEMPLATE)
        pairs = re.findall(r'url "([^"]+)"\s+sha256 "([0-9a-f]{64})"', out)
        self.assertEqual(len(pairs), len(hf.PLATFORMS))
        for url, sha in pairs:
            p = re.fullmatch(rf"https://github\.com/flokkit/apptown/releases/download/v{V}/apptown-{V}-(\w+-\w+)\.tar\.gz", url)
            self.assertIsNotNone(p, url)
            self.assertEqual(sha, digest(p.group(1)), url)

    def test_each_platform_sits_under_its_own_os_and_cpu(self):
        out = hf.formula(V, sums(), TEMPLATE)
        want = {"darwin": "on_macos", "linux": "on_linux", "arm64": "on_arm", "amd64": "on_intel"}
        system = cpu = None
        for line in out.splitlines():
            block = re.match(r"\s*(on_\w+) do$", line)
            if block and block.group(1) in ("on_macos", "on_linux"):
                system = block.group(1)
            elif block:
                cpu = block.group(1)
            url = re.search(r"-(\w+)-(\w+)\.tar\.gz", line)
            if url:
                self.assertEqual((system, cpu), (want[url.group(1)], want[url.group(2)]), line)

    def test_it_installs_the_cli_and_tests_its_version(self):
        out = hf.formula(V, sums(), TEMPLATE)
        self.assertIn('bin.install "bin/apptown"', out)
        self.assertIn('shell_output("#{bin}/apptown version")', out)

    def test_other_files_in_the_sums_are_ignored(self):
        out = hf.formula(V, sums(extra=f"{'0' * 64}  something-else.tar.gz\n"), TEMPLATE)
        self.assertNotIn("0" * 64, out)


class TheFormulaRefuses(unittest.TestCase):
    def refuses(self, text, version=V):
        with self.assertRaises(ValueError) as e:
            hf.formula(version, text, TEMPLATE)
        return str(e.exception)

    def test_a_version_with_its_v(self):
        self.assertIn("without its v", self.refuses(sums(), version="v" + V))

    def test_a_missing_platform(self):
        self.assertIn(f"apptown-{V}-darwin-amd64.tar.gz", self.refuses(sums(platforms=[p for p in hf.PLATFORMS if p != "darwin-amd64"])))

    def test_another_versions_archives(self):
        self.assertIn("no line for", self.refuses(sums(version="1.2.2")))

    def test_a_malformed_line(self):
        self.assertIn("SHA256SUMS:5", self.refuses(sums(extra="abc  apptown.tar.gz\n")))

    def test_a_duplicate(self):
        self.assertIn("listed twice", self.refuses(sums(extra=f"{'1' * 64}  apptown-{V}-linux-arm64.tar.gz\n")))

    def test_a_placeholder_nothing_fills(self):
        with self.assertRaises(ValueError) as e:
            hf.formula(V, sums(), TEMPLATE + '# @windows-amd64@\n')
        self.assertIn("@windows-amd64@", str(e.exception))

    def test_the_command_exits_non_zero_and_prints_nothing(self):
        with tempfile.NamedTemporaryFile("w", suffix="SHA256SUMS", delete=False) as f:
            f.write(sums(platforms=hf.PLATFORMS[:2]))
        self.addCleanup(os.remove, f.name)
        r = subprocess.run([sys.executable, os.path.join(HERE, "homebrew-formula.py"), V, f.name], capture_output=True, text=True)
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.stdout, "")
        self.assertIn("no line for", r.stderr)


class ThePlatforms(unittest.TestCase):
    def test_are_the_ones_the_release_archives_are_built_for(self):
        with open(os.path.join(HERE, "release-archives.sh")) as f:
            m = re.search(r'^platforms="([^"]+)"', f.read(), re.M)
        self.assertIsNotNone(m, "release-archives.sh has no platforms= line")
        built = {p.replace("/", "-") for p in m.group(1).split()}
        self.assertEqual(built, set(hf.PLATFORMS))


if __name__ == "__main__":
    unittest.main()
