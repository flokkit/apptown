"""Tests for scripts/native-install-check.sh without Docker: an AppTown whose
health check answers 503 does not pass however it names its version, and a
cancelled check is not a passed one. The daemon is a stand-in that sleeps;
its health is a local HTTP server."""
import io
import os
import signal
import subprocess
import tarfile
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "native-install-check.sh"


class Down(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        self.send_response(503)
        self.send_header("X-AppTown-Version", "v1.2.3")
        self.end_headers()
        self.wfile.write(b"database unreachable\n")


class NativeInstallCheck(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.work = Path(self.tmp.name)
        self.server = HTTPServer(("127.0.0.1", 0), Down)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def native(self, slow_curl=False):
        bindir = self.work / "bin"
        bindir.mkdir()
        archive = self.work / "apptown-1.2.3-darwin-arm64.tar.gz"
        body = b"#!/bin/sh\nexec sleep 120\n"
        with tarfile.open(archive, "w:gz") as tar:
            entry = tarfile.TarInfo("release/bin/apptownd")
            entry.mode = 0o755
            entry.size = len(body)
            tar.addfile(entry, io.BytesIO(body))
        (bindir / "docker").write_text("#!/bin/sh\nexit 1\n")
        (bindir / "docker").chmod(0o755)
        if slow_curl:
            (bindir / "curl").write_text('#!/bin/sh\ntouch "$READY"\nsleep 1\n')
            (bindir / "curl").chmod(0o755)
        env = {**os.environ, "PATH": str(bindir) + os.pathsep + os.environ["PATH"],
               "NATIVE_CHECK_PORT": str(self.server.server_port), "READY": str(self.work / "ready")}
        return subprocess.Popen(["sh", str(SCRIPT), str(archive)], env=env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

    def test_an_unhealthy_start_fails_though_it_names_the_version(self):
        proc = self.native()
        out, err = proc.communicate(timeout=200)
        self.assertNotEqual(proc.returncode, 0, out + err)
        self.assertIn("did not answer 200", err)

    def test_a_cancelled_check_is_not_a_passed_one(self):
        proc = self.native(slow_curl=True)
        deadline = time.monotonic() + 10
        while not (self.work / "ready").exists() and time.monotonic() < deadline:
            time.sleep(0.01)
        self.assertTrue((self.work / "ready").exists())
        proc.send_signal(signal.SIGTERM)
        out, err = proc.communicate(timeout=10)
        self.assertNotEqual(proc.returncode, 0, out + err)


if __name__ == "__main__":
    unittest.main()
