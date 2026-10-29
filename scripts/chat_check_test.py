"""Tests for scripts/chat-check.py against a fake AppTown: an answer passes,
a helper error, a dead chat or no answer fails, and the chat is ended
either way."""
import json
import os
import subprocess
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "chat-check.py")
MESSAGE = "This is AppTown's release check. Reply with the single word: ready"


class Fake(BaseHTTPRequestHandler):
    answer = None  # the helper message after ours, or None
    state = "detached"
    calls = []

    def log_message(self, *a):
        pass

    def reply(self, body):
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n)) if n else None
        Fake.calls.append(("POST", self.path, body))
        self.reply({"session": {"id": "s1"}} if self.path.endswith("/console") else {})

    def do_GET(self):
        Fake.calls.append(("GET", self.path, None))
        messages = [{"i": 0, "role": "you", "text": MESSAGE}] + ([dict(Fake.answer, i=1)] if Fake.answer else [])
        self.reply({"messages": messages, "state": Fake.state, "turn": ""})

    def do_DELETE(self):
        Fake.calls.append(("DELETE", self.path, None))
        self.send_response(204)
        self.end_headers()


class ChatCheck(unittest.TestCase):
    def setUp(self):
        Fake.answer, Fake.state, Fake.calls = None, "detached", []
        self.server = HTTPServer(("127.0.0.1", 0), Fake)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.shutdown)

    def check(self):
        url = f"http://127.0.0.1:{self.server.server_port}"
        r = subprocess.run(["python3", SCRIPT, url, "release-check", "1"], capture_output=True, text=True,
                           env={**os.environ, "APPTOWN_TOKEN": "t"}, timeout=60)
        self.assertIn(("DELETE", "/api/v1/console/s1", None), Fake.calls, "the chat was not ended")
        return r

    def test_an_answer_passes_and_the_chat_was_placed_on_the_worker(self):
        Fake.answer = {"role": "helper", "text": "ready"}
        r = self.check()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn(("POST", "/api/v1/projects/release-check/console", {"placement": "worker"}), Fake.calls)

    def test_an_error_a_dead_chat_or_silence_fails(self):
        for answer, state, said in (({"role": "helper", "text": "model unavailable", "error": True}, "detached", "error"),
                                    (None, "dead", "dead"), (None, "detached", "no answer")):
            with self.subTest(said=said):
                Fake.answer, Fake.state, Fake.calls = answer, state, []
                r = self.check()
                self.assertEqual(r.returncode, 1)
                self.assertIn(said, r.stderr)


if __name__ == "__main__":
    unittest.main()
