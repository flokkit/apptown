"""site/try.sh and site/publish.sh under /bin/sh and /bin/dash, against a fake
Docker, browser and AWS CLI: piped input, the ready link, restarts, and that a
failed stop or removal is never reported as done."""
import os
from pathlib import Path
import pty
import shutil
import subprocess
import tempfile
import select
import time
import unittest

ROOT = Path(os.environ.get('SITE_TRY_ROOT', Path(__file__).resolve().parents[1]))
SHELLS = ['/bin/sh', '/bin/dash']

DOCKER = '''#!/bin/sh
printf '%s\\n' "$*" >> "$CALLS"
case "$1" in
info) [ "$#" = 1 ] || echo 17179869184 ;;
inspect)
  case "$3" in
  *State.Status*) echo "$INITIAL_STATE" ;;
  *) echo true ;;
  esac ;;
run|start) : ;;
logs)
  if [ "$INITIAL_STATE" = exited ]; then
    echo 'apptown: AppTown is ready at http://localhost:7777'
  else
    echo 'apptown: AppTown is ready. Open http://localhost:7777/?setup=abc123 to create the administrator.'
  fi ;;
stop) echo 'Error: cannot stop container: permission denied' >&2; exit 1 ;;
rm) : ;;
volume)
  case "$2" in
  inspect) : ;;
  *) echo 'Error: volume is in use' >&2; exit 1 ;;
  esac ;;
images) echo ghcr.io/flokkit/apptown:1.0.0 ;;
rmi) : ;;
esac
'''

class TryScript(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.bin = self.base / 'bin'
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=str(self.bin) + ':' + os.environ['PATH'],
                        CALLS=str(self.base / 'calls'), APPTOWN_VERSION='1.0.0', INITIAL_STATE='none')
        self.stub('docker', DOCKER)
        self.stub('open', '#!/bin/sh\nprintf "%s\\n" "$1" >> "$BROWSER_CALLS"\n')
        self.env['BROWSER_CALLS'] = str(self.base / 'browser')

    def stub(self, name, body):
        path = self.bin / name
        path.write_text(body)
        path.chmod(0o755)

    def run_script(self, shell, arg='start', terminal_answer=None):
        source = (ROOT / 'site/try.sh').read_text()
        if terminal_answer is None:
            result = subprocess.run([shell, '-s', '--', arg], input=source,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    env=self.env, text=True, timeout=10)
            return result.returncode, result.stdout
        env = dict(self.env, SOURCE=str(ROOT / 'site/try.sh'), TEST_SHELL=shell)
        pid, master = pty.fork()
        if pid == 0:
            os.execve(shell, [shell, '-c', 'cat "$SOURCE" | "$TEST_SHELL" -s -- uninstall'], env)
        out = b''
        answered = False
        status = None
        try:
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                if select.select([master], [], [], 0.1)[0]:
                    try:
                        data = os.read(master, 65536)
                    except OSError:
                        break
                    if not data:
                        break
                    out += data
                    if b'[y/N]' in out and not answered:
                        os.write(master, terminal_answer.encode())
                        answered = True
                done, candidate = os.waitpid(pid, os.WNOHANG)
                if done:
                    status = candidate
                    break
            else:
                os.kill(pid, 9)
                self.fail('uninstall timed out')
        finally:
            os.close(master)
            if status is None:
                _, status = os.waitpid(pid, 0)
        return os.waitstatus_to_exitcode(status), out.decode()

    def test_piped_start_opens_setup_link(self):
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell)
                self.assertEqual(status, 0, out)
                self.assertIn('AppTown is ready: http://localhost:7777/?setup=abc123', out)
                self.assertIn('?setup=abc123', (self.base / 'browser').read_text())

    def test_stopped_container_restarts_and_uses_ready_link(self):
        self.env['INITIAL_STATE'] = 'exited'
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell)
                self.assertEqual(status, 0, out)
                self.assertIn('start apptown', (self.base / 'calls').read_text())
                self.assertIn('AppTown is ready: http://localhost:7777', out)
                self.assertIn('logs --since ', (self.base / 'calls').read_text())

    def test_declined_uninstall_does_not_remove_anything(self):
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell, 'uninstall', 'n\n')
                self.assertEqual(status, 0, out)
                self.assertIn('nothing removed.', out)
                self.assertNotIn('rm ', (self.base / 'calls').read_text())

    def test_stop_failure_is_not_reported_as_stopped(self):
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell, 'stop')
                self.assertNotEqual(status, 0, out)
                self.assertNotIn('not running.', out)

    def test_failed_volume_removal_is_not_reported_as_complete(self):
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell, 'uninstall', 'y\n')
                self.assertIn('volume rm apptown', (self.base / 'calls').read_text())
                self.assertNotEqual(status, 0, out)
                self.assertNotIn('removed the container, its data and its image.', out)

    def test_inspect_error_is_not_reported_as_absence_or_removal(self):
        self.stub('docker', DOCKER.replace(
            'inspect)\n  case',
            "inspect) echo 'Error: cannot connect to Docker daemon' >&2; exit 1\n  case"
        ).replace('  inspect) : ;;',
                  "  inspect) echo 'Error: cannot connect to Docker daemon' >&2; exit 1 ;;"
        ).replace('images) echo ghcr.io/flokkit/apptown:1.0.0 ;;', 'images) : ;;'))
        for shell in SHELLS:
            for command, answer in [('stop', None), ('uninstall', 'y\n')]:
                with self.subTest(shell=shell, command=command):
                    status, out = self.run_script(shell, command, answer)
                    self.assertNotEqual(status, 0, out)
                    self.assertNotIn('not installed; nothing to stop.', out)
                    self.assertNotIn('removed the container, its data and its image.', out)

    def test_missing_daemon_socket_is_not_reported_as_absent_container(self):
        self.stub('docker', DOCKER.replace(
            'inspect)\n  case',
            "inspect) echo 'failed to connect to the docker API at unix:///private/tmp/apptown-review-missing-daemon.sock; check if the path is correct and if the daemon is running: dial unix /private/tmp/apptown-review-missing-daemon.sock: connect: no such file or directory' >&2; exit 1\n  case"
        ))
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell, 'stop')
                self.assertNotIn('not installed; nothing to stop.', out)
                self.assertNotEqual(status, 0, out)

    # Docker's own not-found wording (Docker 29, 20 Oct 2026) is absence:
    # stop has nothing to do, and uninstall removes what is left.
    def test_docker_not_found_answers_are_absence(self):
        self.stub('docker', DOCKER.replace(
            'inspect)\n  case',
            "inspect) echo 'Error: no such object: apptown' >&2; exit 1\n  case"
        ).replace("  inspect) : ;;", "  inspect) echo 'Error response from daemon: get apptown: no such volume' >&2; exit 1 ;;"))
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell, 'stop')
                self.assertEqual(status, 0, out)
                self.assertIn('not installed; nothing to stop.', out)
                status, out = self.run_script(shell, 'uninstall', 'y\n')
                self.assertEqual(status, 0, out)
                self.assertIn('removed the container, its data and its image.', out)
                calls = (self.base / 'calls').read_text()
                self.assertNotIn('rm -f apptown', calls)
                self.assertNotIn('volume rm apptown', calls)

    def test_image_listing_error_is_not_reported_as_complete_removal(self):
        self.stub('docker', DOCKER.replace(
            "*) echo 'Error: volume is in use' >&2; exit 1 ;;", '*) : ;;'
        ).replace('images) echo ghcr.io/flokkit/apptown:1.0.0 ;;',
                  "images) echo 'Error: cannot connect to Docker daemon' >&2; exit 1 ;;"))
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell, 'uninstall', 'y\n')
                self.assertNotEqual(status, 0, out)
                self.assertNotIn('removed the container, its data and its image.', out)

    def test_publish_substitution_metadata_and_invalidation(self):
        self.stub('aws', '''#!/bin/sh
printf '%s\\n' "$*" >> "$CALLS"
if [ "$1 $2" = 's3 cp' ]; then
  cp "$3" "$PUBLISHED/$(basename "$4")"
fi
''')
        self.env.update(SITE_BUCKET='review-bucket', SITE_DISTRIBUTION='review-distribution', PUBLISHED=str(self.base))
        for shell in SHELLS:
            with self.subTest(shell=shell):
                result = subprocess.run([shell, str(ROOT / 'site/publish.sh'), '1.2.3-rc.1'], env=self.env,
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                published = (self.base / 'try.sh').read_text()
                self.assertNotIn('@VERSION@', published)
                self.assertIn('VERSION=${APPTOWN_VERSION:-1.2.3-rc.1}', published)
                self.assertEqual((self.base / 'index.html').read_bytes(), (ROOT / 'site/index.html').read_bytes())
                calls = (self.base / 'calls').read_text()
                self.assertIn('--content-type text/html; charset=utf-8', calls)
                self.assertIn('--content-type text/plain; charset=utf-8', calls)
                self.assertIn('--paths / /index.html /try /try.sh', calls)

if __name__ == '__main__':
    unittest.main(verbosity=2)
