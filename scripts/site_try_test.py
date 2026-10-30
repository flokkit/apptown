"""site/try.sh and site/publish.sh under /bin/sh and /bin/dash, against a fake
Docker, browser and AWS CLI: piped input, the ready link, restarts, and that a
failed stop or removal is never reported as done."""
import os
import json
import http.server
import threading
import functools
from pathlib import Path
import pty
import shutil
import subprocess
import tempfile
import re
import select
import struct
import time
import unittest
import zlib

ROOT = Path(os.environ.get('SITE_TRY_ROOT', Path(__file__).resolve().parents[1]))
SHELLS = ['/bin/sh', '/bin/dash']
DONE = 'removed the container, its data, its image and the trust in its certificate.'

DOCKER = '''#!/bin/sh
printf '%s\\n' "$*" >> "$CALLS"
[ "$1" != container ] || shift
case "$1" in
info) [ "$#" = 1 ] || echo 17179869184 ;;
inspect)
  case "$3" in
  *State.Status*) echo "$INITIAL_STATE" ;;
  *) echo true ;;
  esac ;;
run|start) : ;;
exec) cat "$CA_PEM" ;;
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
        # The trust step never reaches a real keychain, system store or browser store.
        self.env['APPTOWN_HOME'] = str(self.base / 'home')
        self.env['HOME'] = str(self.base)
        self.env['UNAME'] = 'Darwin'
        self.stub('uname', '#!/bin/sh\necho "$UNAME"\n')
        # A real certificate, so fingerprints are computed as in use.
        self.env['CA_PEM'] = str(self.base / 'ca.pem')
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
                        '-nodes', '-keyout', str(self.base / 'ca.key'), '-out', self.env['CA_PEM'],
                        '-days', '1', '-subj', '/CN=AppTown local CA test'], check=True, capture_output=True)
        self.fp = subprocess.run(['openssl', 'x509', '-noout', '-fingerprint', '-sha1', '-in', self.env['CA_PEM']],
                                 check=True, capture_output=True, text=True).stdout.split('=')[1].strip().replace(':', '')
        for tool in ('security', 'sudo', 'certutil', 'update-ca-certificates'):
            self.stub(tool, '#!/bin/sh\nprintf "%s %s\\n" "$(basename "$0")" "$*" >> "$TRUST_CALLS"\nexit "${TRUST_FAIL:-0}"\n')
        self.env['TRUST_CALLS'] = str(self.base / 'trust')
        # security answers find-certificate with the hash of what is trusted.
        self.stub('security', '#!/bin/sh\nprintf "security %s\\n" "$*" >> "$TRUST_CALLS"\n'
                  'case "$1" in find-certificate) [ -n "$FIND_FAIL" ] && { echo "keychain locked" >&2; exit 1; }; [ -n "$TRUSTED_FP" ] && echo "SHA-1 hash: $TRUSTED_FP" ;; '
                  'add-trusted-cert|delete-certificate) exit "${TRUST_FAIL:-0}" ;; esac\nexit 0\n')
        self.env['TRUSTED_FP'] = ''
        self.env['FIND_FAIL'] = ''
        self.stub('open', '#!/bin/sh\nprintf "%s\\n" "$1" >> "$BROWSER_CALLS"\n')
        self.env['BROWSER_CALLS'] = str(self.base / 'browser')

    def stub(self, name, body):
        path = self.bin / name
        path.write_text(body)
        path.chmod(0o755)

    def run_script(self, shell, arg='start', terminal_answer=None):
        source = (ROOT / 'site/try.sh').read_text()
        if terminal_answer is None:
            # A session of its own: no controlling terminal, so the script's
            # questions on /dev/tty go unanswered here even when the tests run
            # in someone's terminal (they would otherwise wait for a reply).
            result = subprocess.run([shell, '-s', '--', arg], input=source,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    env=self.env, text=True, timeout=10,
                                    start_new_session=True)
            return result.returncode, result.stdout
        env = dict(self.env, SOURCE=str(ROOT / 'site/try.sh'), TEST_SHELL=shell)
        pid, master = pty.fork()
        if pid == 0:
            os.execve(shell, [shell, '-c', 'cat "$SOURCE" | "$TEST_SHELL" -s -- "$ARG"'], dict(env, ARG=arg))
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
                    # The script can exit with its last line still in the
                    # terminal's buffer, unread: read it to the end (the
                    # read fails or returns nothing once it is empty).
                    while select.select([master], [], [], 0.5)[0]:
                        try:
                            data = os.read(master, 65536)
                        except OSError:
                            break
                        if not data:
                            break
                        out += data
                    break
            else:
                os.kill(pid, 9)
                self.fail(arg + ' timed out')
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

    def test_retained_volume_is_reused_when_container_is_missing(self):
        # Untyped inspect finds the retained volume; only container inspect
        # can establish whether the trial itself is installed.
        self.stub('docker', DOCKER.replace('[ "$1" != container ] || shift', """
if [ ! -f "$CALLS.container" ]; then
  case "$1 $2" in
  'container inspect') echo 'Error: no such container: apptown' >&2; exit 1 ;;
  'inspect -f') echo 'template: map has no entry for key "State"' >&2; exit 1 ;;
  'inspect apptown') echo '[{"Name":"apptown"}]'; exit 0 ;;
  'run -d') touch "$CALLS.container" ;;
  esac
fi
[ "$1" != container ] || shift
"""))
        for shell in SHELLS:
            with self.subTest(shell=shell):
                (self.base / 'calls').unlink(missing_ok=True)
                (self.base / 'calls.container').unlink(missing_ok=True)
                status, out = self.run_script(shell)
                self.assertEqual(status, 0, out)
                calls = (self.base / 'calls').read_text()
                self.assertIn('run -d --name apptown', calls)
                self.assertIn('-v apptown:/var/lib/apptown', calls)
                self.assertNotIn('volume rm', calls)
                self.assertNotIn('start apptown', calls)

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
                self.assertNotIn(DONE, out)

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
                    self.assertNotIn(DONE, out)

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
                self.assertIn(DONE, out)
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
                self.assertNotIn(DONE, out)

    def site_with_tour(self):
        """A copy of the tree publish.sh reads, with a fake screenshot for
        each stop: the shots are not in the tree (the release workflow makes
        them)."""
        repo = self.base / 'repo'
        for rel in ('site/publish.sh', 'site/index.html', 'site/try.sh', 'site/tour/index.html',
                    'site/tour/tour.yaml', 'scripts/tour-check.py', 'scripts/publish-tour.py', 'deploy/one-server/compose.yaml',
                    'deploy/split-install/apptown/compose.yaml', 'deploy/split-install/worker/compose.yaml'):
            (repo / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(ROOT / rel, repo / rel)
        ids = re.findall(r'<section id="([^"]+)">', (ROOT / 'site/tour/index.html').read_text())
        def chunk(kind, data):
            return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data) & 0xffffffff)
        shot = (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 2880, 1800, 8, 0, 0, 0, 0))
                + chunk(b'IDAT', zlib.compress(b'\x00')) + chunk(b'IEND', b''))
        for sid in ids:
            for theme in ('light', 'dark'):
                (repo / 'site/tour' / f'{sid}-{theme}.png').write_bytes(shot)
        return repo, ids

    def test_publish_substitution_metadata_and_invalidation(self):
        repo, ids = self.site_with_tour()
        self.stub('aws', """#!/usr/bin/env python3
import os, sys, shutil
from pathlib import Path
args = sys.argv[1:]
with open(os.environ['CALLS'], 'a') as f: f.write(' '.join(args) + '\\n')
if args[:2] == ['s3', 'cp']:
    key = args[3].split('/', 3)[3]
    target = Path(os.environ['PUBLISHED']) / key
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(args[2], target)
elif args[:2] == ['s3api', 'put-object']:
    key = args[args.index('--key') + 1]
    target = Path(os.environ['PUBLISHED']) / key
    if '--if-none-match' in args and target.exists():
        print('PreconditionFailed', file=sys.stderr)
        sys.exit(1)
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(args[args.index('--body') + 1], target)
""")
        (repo / 'site/tour/metadata.json').write_text(json.dumps({'kind': 'release', 'version': 'v1.2.3-rc.1', 'source': 'a' * 40}))
        (self.base / 'tour').mkdir()
        (self.base / 'tour/versions.json').write_text('[]')
        handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(self.base))
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        self.env.update(SITE_BUCKET='review-bucket', SITE_DISTRIBUTION='review-distribution', PUBLISHED=str(self.base),
                        GITHUB_SHA='a' * 40, SITE_URL='http://127.0.0.1:' + str(server.server_port))
        for shell in SHELLS:
            with self.subTest(shell=shell):
                result = subprocess.run([shell, str(repo / 'site/publish.sh'), '1.2.3-rc.1'], env=self.env,
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                published = (self.base / 'try.sh').read_text()
                self.assertNotIn('@VERSION@', published)
                self.assertIn('VERSION=${APPTOWN_VERSION:-1.2.3-rc.1}', published)
                self.assertEqual((self.base / 'index.html').read_bytes(), (ROOT / 'site/index.html').read_bytes())
                calls = (self.base / 'calls').read_text()
                self.assertIn('--content-type text/html; charset=utf-8', calls)
                self.assertIn('--content-type text/plain; charset=utf-8', calls)
                self.assertIn('--paths / /index.html /try /try.sh /compose.yaml /split-install/* /tour/*', calls)
                # The one-server compose file starts the same release.
                compose = (self.base / 'compose.yaml').read_text()
                self.assertNotIn('@VERSION@', compose)
                self.assertIn('ghcr.io/flokkit/apptown:${APPTOWN_VERSION:-1.2.3-rc.1}', compose)
                # So do the split install's, at their repository paths.
                for machine, image in (('apptown', 'apptown'), ('worker', 'apptown-worker')):
                    split = (self.base / 'split-install' / machine / 'compose.yaml').read_text()
                    self.assertNotIn('@VERSION@', split)
                    self.assertIn(f'ghcr.io/flokkit/{image}:${{APPTOWN_VERSION:-1.2.3-rc.1}}', split)
                self.assertIn('--content-type text/yaml; charset=utf-8', calls)
                # The complete snapshot is immutable and history points to it.
                snapshot = self.base / 'tour/v1.2.3-rc.1'
                self.assertIn('AppTown tour · v1.2.3-rc.1', (snapshot / 'index.html').read_text())
                self.assertIn('/tour/v1.2.3-rc.1/', (self.base / 'tour/index.html').read_text())
                self.assertIn('s3api put-object', calls)
                self.assertIn('--if-none-match *', calls)
                self.assertNotIn('s3 sync', calls)
                self.assertNotIn('s3 ls', calls)
                self.assertIn('--content-type image/png', calls)
                # Identical fixture images deliberately share their content hash.
                self.assertEqual(len(list(snapshot.glob('*.png'))), 1)

    # A tour missing a screenshot is not published: nothing is uploaded.
    def test_publish_refuses_a_tour_without_its_shots(self):
        repo, ids = self.site_with_tour()
        (repo / 'site/tour' / f'{ids[0]}-dark.png').unlink()
        self.stub('aws', '#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALLS"\n')
        for shell in SHELLS:
            with self.subTest(shell=shell):
                result = subprocess.run([shell, str(repo / 'site/publish.sh'), '1.2.3-rc.1'], env=self.env,
                                        capture_output=True, text=True, timeout=10)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(f'no {ids[0]}-dark.png', result.stderr)
                self.assertIn('run make tour first', result.stderr)
                calls = self.base / 'calls'
                self.assertNotIn('s3 ', calls.read_text() if calls.exists() else '')


    def trust_calls(self):
        path = self.base / 'trust'
        return path.read_text() if path.exists() else ''

    # Starting asks first, then adds the trial's authority to the login
    # keychain and keeps a copy for uninstall; declining adds nothing.
    def test_start_asks_then_trusts_the_certificate_on_macos(self):
        for shell in SHELLS:
            for answer, added in [('y\n', True), ('n\n', False)]:
                with self.subTest(shell=shell, answer=answer.strip()):
                    (self.base / 'trust').unlink(missing_ok=True)
                    status, out = self.run_script(shell, 'start', answer)
                    self.assertEqual(status, 0, out)
                    self.assertIn('never for a real website', out)
                    calls = self.trust_calls()
                    self.assertEqual('add-trusted-cert' in calls, added, calls)
                    self.assertTrue((self.base / 'home' / 'apptown-local-ca.pem').exists())

    # An authority the keychain already trusts is not asked about again.
    def test_an_already_trusted_certificate_is_not_asked_about(self):
        self.env['TRUSTED_FP'] = self.fp
        for shell in SHELLS:
            with self.subTest(shell=shell):
                status, out = self.run_script(shell)
                self.assertEqual(status, 0, out)
                self.assertNotIn('Trust AppTown', out)
                self.assertNotIn('add-trusted-cert', self.trust_calls())

    # Uninstall takes the trust away by the kept copy's fingerprint; a
    # keychain that refuses is reported, never claimed done.
    def test_uninstall_removes_the_trust_or_says_it_could_not(self):
        home = self.base / 'home'
        home.mkdir()
        (home / 'apptown-local-ca.pem').write_text(Path(self.env['CA_PEM']).read_text())
        self.env['TRUSTED_FP'] = self.fp
        self.stub('docker', DOCKER.replace("*) echo 'Error: volume is in use' >&2; exit 1 ;;", '*) : ;;'))
        for shell in SHELLS:
            for fail, ok in [('0', True), ('1', False)]:
                with self.subTest(shell=shell, fail=fail):
                    (home / 'apptown-local-ca.pem').write_text(Path(self.env['CA_PEM']).read_text())
                    self.env['TRUST_FAIL'] = fail
                    status, out = self.run_script(shell, 'uninstall', 'y\n')
                    self.assertIn('delete-certificate -t -Z ' + self.fp, self.trust_calls())
                    self.assertEqual(status == 0, ok, out)
                    self.assertEqual(DONE in out, ok, out)
                    self.assertEqual((home / 'apptown-local-ca.pem').exists(), not ok)
        self.env['TRUST_FAIL'] = '0'

    # A keychain that cannot be read is not one without the certificate:
    # uninstall keeps the copy and does not claim the trust is gone.
    def test_an_unreadable_keychain_is_not_removal(self):
        home = self.base / 'home'
        home.mkdir()
        self.stub('docker', DOCKER.replace("*) echo 'Error: volume is in use' >&2; exit 1 ;;", '*) : ;;'))
        self.env['FIND_FAIL'] = '1'
        for shell in SHELLS:
            with self.subTest(shell=shell):
                (home / 'apptown-local-ca.pem').write_text(Path(self.env['CA_PEM']).read_text())
                status, out = self.run_script(shell, 'uninstall', 'y\n')
                self.assertNotEqual(status, 0, out)
                self.assertNotIn(DONE, out)
                self.assertIn('could not read your login keychain', out)
                self.assertTrue((home / 'apptown-local-ca.pem').exists())

    # On Linux the script changes no store: it explains and prints the
    # commands, and uninstall says the trust may remain, never that it is gone.
    def test_linux_prints_the_steps_and_changes_no_store(self):
        self.env['UNAME'] = 'Linux'
        self.stub('docker', DOCKER.replace("*) echo 'Error: volume is in use' >&2; exit 1 ;;", '*) : ;;'))
        for shell in SHELLS:
            with self.subTest(shell=shell):
                (self.base / 'trust').unlink(missing_ok=True)
                status, out = self.run_script(shell)
                self.assertEqual(status, 0, out)
                self.assertIn('never for a real website', out)
                self.assertIn('#trust-the-trials-certificate', out)
                status, out = self.run_script(shell, 'uninstall', 'y\n')
                self.assertEqual(status, 0, out)
                self.assertIn('may still be trusted', out)
                self.assertNotIn(DONE, out)
                self.assertEqual(self.trust_calls(), '')

if __name__ == '__main__':
    unittest.main(verbosity=2)
