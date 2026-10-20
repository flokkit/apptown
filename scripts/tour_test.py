"""scripts/tour-check.py and scripts/tour-stamp.py, on throwaway trees."""
import os
import struct
import subprocess
import sys
import tempfile
import unittest
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))


def png(path, w, h):
    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xffffffff)
    raw = b"".join(b"\x00" + b"\x00" * w for _ in range(1))
    data = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 0, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")
    with open(path, "wb") as f:
        f.write(data)


TOUR = """# stops
stops:
  - id: home
    title: Home
    caption: "Where every person starts: the changes that need you."
    path: /
  - id: people
    title: People
    caption: Who can do what.
    path: /people
"""


class TourTest(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.tour = os.path.join(self.root, "site", "tour")
        os.makedirs(self.tour)
        with open(os.path.join(self.tour, "tour.yaml"), "w") as f:
            f.write(TOUR)
        for sid in ("home", "people"):
            for t in ("light", "dark"):
                png(os.path.join(self.tour, f"{sid}-{t}.png"), 2880, 1800)
        with open(os.path.join(self.tour, "index.html"), "w") as f:
            f.write('<section id="home"></section><section id="people"></section>')
        for d in ("web/src", "internal", "web/dist"):
            os.makedirs(os.path.join(self.root, d))
        with open(os.path.join(self.root, "web/src/App.tsx"), "w") as f:
            f.write("app")
        self.env = dict(os.environ, TOUR_ROOT=self.root)

    def run_script(self, *args):
        return subprocess.run([sys.executable, os.path.join(HERE, args[0])] + list(args[1:]), env=self.env,
                              capture_output=True, text=True)

    # A complete tour passes; each way it can be wrong fails, naming the problem.
    def test_the_check_names_each_way_a_tour_is_incomplete(self):
        self.assertEqual(self.run_script("tour-check.py").returncode, 0)
        cases = {
            "no people-dark.png": lambda: os.remove(os.path.join(self.tour, "people-dark.png")),
            "not 2880x1800": lambda: png(os.path.join(self.tour, "home-light.png"), 1440, 900),
            "belongs to no stop": lambda: png(os.path.join(self.tour, "old-light.png"), 2880, 1800),
            "index.html shows": lambda: open(os.path.join(self.tour, "index.html"), "w").write('<section id="home"></section>'),
            "no caption": lambda: open(os.path.join(self.tour, "tour.yaml"), "w").write(TOUR.replace("    caption: Who can do what.\n", "")),
        }
        for want, breakit in cases.items():
            with self.subTest(want=want):
                self.setUp()
                breakit()
                r = self.run_script("tour-check.py")
                self.assertEqual(r.returncode, 1, r.stderr)
                self.assertIn(want, r.stderr)

    def test_the_gate_rejects_malformed_yaml(self):
        # An ordinary caption edit leaves an unquoted colon-space. The
        # camera's yaml.Unmarshal rejects it; the cheap gate must too.
        path = os.path.join(self.tour, "tour.yaml")
        with open(path) as f:
            text = f.read()
        text = text.replace(
            'caption: "Where every person starts: the changes that need you."',
            'caption: Where every person starts: the changes that need you.')
        with open(path, "w") as f:
            f.write(text)
        r = self.run_script("tour-check.py")
        self.assertNotEqual(r.returncode, 0, "malformed tour.yaml passed the gate")
        self.assertIn("quote a value with a colon", r.stderr)
        # Other text outside the shape YAML reads the same way is refused too.
        for bad in ('    title: [Home]', "    title: 'Home'", '    colour: green', '    title: Home # first'):
            with self.subTest(line=bad):
                with open(path, "w") as f:
                    f.write(TOUR.replace("    title: Home", bad))
                self.assertEqual(self.run_script("tour-check.py").returncode, 1, bad)

    def test_the_gate_rejects_duplicate_yaml_keys(self):
        # Copying a title while editing a stop must not silently replace it:
        # the camera's yaml.Unmarshal rejects duplicate mapping keys.
        path = os.path.join(self.tour, "tour.yaml")
        with open(path, "w") as f:
            f.write(TOUR.replace("    title: Home\n",
                                 "    title: First title\n    title: Home\n"))
        r = self.run_script("tour-check.py")
        self.assertNotEqual(r.returncode, 0, "duplicate YAML key passed the gate")

    # The stamp is current after write; a change, addition or removal in an
    # input makes it stale; the tour's own outputs and web/dist do not.
    def test_the_stamp_goes_stale_with_its_inputs_only(self):
        self.assertEqual(self.run_script("tour-stamp.py", "check").returncode, 1)  # no stamp yet
        self.assertEqual(self.run_script("tour-stamp.py", "write").returncode, 0)
        self.assertEqual(self.run_script("tour-stamp.py", "check").returncode, 0)
        png(os.path.join(self.tour, "home-light.png"), 2880, 1800)
        open(os.path.join(self.root, "web/dist/x.js"), "w").write("built")
        self.assertEqual(self.run_script("tour-stamp.py", "check").returncode, 0)
        for change in (lambda: open(os.path.join(self.root, "web/src/App.tsx"), "w").write("changed"),
                       lambda: open(os.path.join(self.root, "internal/new.go"), "w").write("package x"),
                       lambda: open(os.path.join(self.tour, "tour.yaml"), "a").write("\n")):
            self.run_script("tour-stamp.py", "write")
            change()
            r = self.run_script("tour-stamp.py", "check")
            self.assertEqual(r.returncode, 1)
            self.assertIn("run make tour", r.stderr)


if __name__ == "__main__":
    unittest.main()
