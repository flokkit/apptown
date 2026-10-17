"""stranger-install.sh changes nothing on the host but the Docker daemon it
starts for the run: every effect on the host's Docker is starting that one
container, copying into it, running inside it, or removing it at the end,
whether the run passes or fails. Nothing is chosen by when it was created."""
import json
import os
import signal
import shutil
import stat
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

HERE = os.path.dirname(os.path.abspath(__file__))
# A hang guard only: a run takes a few seconds alone, but the full gate runs
# these beside every Go suite and a 20 s deadline timed out there.
HANG_GUARD = 120
STUB = r"""#!/bin/sh
echo "$*" >> "$CALLS"
case "$1" in
run) [ "$2" = -d ] && echo run-daemon-1 ;;
exec) case "$*" in *"debian:stable-slim"*) exit "${INSIDE_EXIT:-0}" ;; esac ;;
esac
exit 0
"""


class StrangerTouchesOnlyItsOwnDaemon(unittest.TestCase):
    def run_script(self, inside_exit):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp)
        bindir = os.path.join(tmp, "bin")
        os.makedirs(bindir)
        docker = os.path.join(bindir, "docker")
        with open(docker, "w") as f:
            f.write(STUB)
        os.chmod(docker, os.stat(docker).st_mode | stat.S_IEXEC)
        archive = os.path.join(tmp, "forge-v1-linux-amd64.tar.gz")
        open(archive, "w").close()
        calls = os.path.join(tmp, "calls")
        env = {**os.environ, "PATH": bindir + os.pathsep + os.environ["PATH"], "CALLS": calls, "INSIDE_EXIT": str(inside_exit)}
        r = subprocess.run(["sh", os.path.join(HERE, "stranger-install.sh"), archive], env=env, capture_output=True, text=True)
        with open(calls) as f:
            return r.returncode, [line.split() for line in f.read().splitlines()]

    def assert_only_its_daemon(self, calls):
        self.assertEqual(calls[0][:4], ["run", "-d", "--privileged", "docker:27-dind"])
        for c in calls[1:]:
            if c[0] == "exec":
                self.assertEqual(c[1], "run-daemon-1", c)  # inside the run's own daemon
            elif c[0] == "cp":
                self.assertTrue(c[-1].startswith("run-daemon-1:"), c)
            elif c[0] == "rm":
                self.assertEqual(c, ["rm", "-f", "-v", "run-daemon-1"])
            else:
                self.fail("an effect on the host's Docker outside the run's daemon: %s" % c)
        self.assertEqual(calls[-1], ["rm", "-f", "-v", "run-daemon-1"])

    def test_a_passing_run(self):
        code, calls = self.run_script(0)
        self.assertEqual(code, 0)
        self.assert_only_its_daemon(calls)

    def test_a_failing_run_still_removes_only_its_daemon(self):
        code, calls = self.run_script(3)
        self.assertNotEqual(code, 0)
        self.assert_only_its_daemon(calls)



CHANGE_ID = "01a135dc-09bd-7006-9ede-c0ee3598f0a7"
OTHER_ID = "01a135dc-09bd-7007-9ede-c0ee3598f0a7"
INSIDE_STUB = r"""#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
cmd, args = Path(sys.argv[0]).name, sys.argv[1:]
work = Path(os.environ['FIXTURE_WORK'])
with open(work / 'inside-calls', 'a') as f:
    # Never record Authorization, setup token or password, even in this fixture.
    json.dump({'command': cmd, 'args': [args[-1]] if cmd == 'curl' else args}, f)
    f.write('\n')
if cmd == 'apt-get':
    sys.exit(0)
if cmd == 'docker':
    print('docker fixture')
elif cmd == 'git':
    if 'bundle' in args and 'create' in args:
        Path(args[args.index('create') + 1]).touch()
elif cmd == 'forged':
    if args == ['-version']:
        print('forged fixture-release')
    else:
        (work / 'forged-pid').write_text(str(os.getpid()))
        os.execl('/bin/sleep', 'sleep', '300')
elif cmd == 'forge':
    if args and args[0] == 'preview':
        print('the preview is up: http://fixture.invalid/page')
elif cmd == 'curl':
    url = args[-1]
    if url.endswith('/healthz'):
        print('ok')
    elif url.endswith('/auth/bootstrap'):
        (work / 'data' / 'setup-token').unlink()
        print('{}')
    elif url.endswith('/tokens'):
        print('{"token":"fixture-token"}')
    elif url.endswith('/changes'):
        print((work / 'change-response').read_text())
        sys.exit(int(os.environ.get('CHANGE_HTTP_EXIT', '0')))
    elif url == 'http://fixture.invalid/page':
        print(os.environ.get('FIXTURE_PAGE', 'Hello from a stranger.'))
    else:
        sys.exit('unexpected curl destination')
else:
    sys.exit('unexpected fixture command ' + cmd)
"""


class StrangerReadsTheAdmittedChange(unittest.TestCase):
    def run_inside(self, response, http_exit=0, page='Hello from a stranger.'):
        tmp = tempfile.TemporaryDirectory(prefix='forge-stranger-inside-')
        self.addCleanup(tmp.cleanup)
        work = Path(tmp.name)
        bindir = work / 'path-bin'
        bindir.mkdir()
        for name in ('apt-get', 'docker', 'git', 'curl'):
            command = bindir / name
            command.write_text(INSIDE_STUB)
            command.chmod(0o755)
        package = work / 'package' / 'forge-fixture'
        (package / 'bin').mkdir(parents=True)
        for name in ('forge', 'forged'):
            command = package / 'bin' / name
            command.write_text(INSIDE_STUB)
            command.chmod(0o755)
        archive = work / 'forge-fixture.tar.gz'
        with tarfile.open(archive, 'w:gz') as tar:
            tar.add(package, arcname='forge-fixture')
        (work / 'data').mkdir()
        (work / 'data' / 'setup-token').write_text('fixture-setup-token')
        (work / 'change-response').write_text(response)
        env = {**os.environ, 'PATH': str(bindir) + os.pathsep + os.environ['PATH'],
               'FIXTURE_WORK': str(work), 'CHANGE_HTTP_EXIT': str(http_exit), 'FIXTURE_PAGE': page}
        stopped = False
        try:
            result = subprocess.run(['sh', os.path.join(HERE, 'stranger-install.sh'), '--inside', str(archive)],
                                    env=env, capture_output=True, text=True, timeout=HANG_GUARD)
        finally:
            pid_file = work / 'forged-pid'
            if pid_file.exists():
                pid = int(pid_file.read_text())
                try:
                    os.kill(pid, 0)
                except ProcessLookupError:
                    stopped = True
                else:
                    # Stop only this fixture's recorded process on a failure.
                    os.kill(pid, signal.SIGTERM)
        self.assertTrue(stopped, 'installer left its own forged process running')
        calls = [json.loads(line) for line in (work / 'inside-calls').read_text().splitlines()]
        return result, calls

    def assert_preview(self, calls):
        installs = [c['args'] for c in calls if c['command'] == 'apt-get' and c['args'][0] == 'install']
        self.assertTrue(any('python3' in args for args in installs), 'fresh disposable host did not install its JSON parser')
        self.assertIn({'command': 'forge', 'args': ['preview', CHANGE_ID]}, calls)
        self.assertIn({'command': 'curl', 'args': ['http://fixture.invalid/page']}, calls)

    def test_the_current_uuid_response_reaches_and_checks_the_first_preview(self):
        result, calls = self.run_inside(json.dumps({'change': {'id': CHANGE_ID, 'number': 1}, 'run': None}, separators=(',', ':')))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assert_preview(calls)
        self.assertIn('done in ', result.stdout)
        self.assertIn('(budget 900s)', result.stdout)

    def test_reordered_pretty_response_does_not_select_an_unrelated_id(self):
        response = json.dumps({'id': OTHER_ID, 'run': {'id': OTHER_ID}, 'change': {'number': 1, 'id': CHANGE_ID}}, indent=2)
        result, calls = self.run_inside(response)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assert_preview(calls)

    def test_missing_invalid_and_wrongly_typed_change_ids_never_reach_preview(self):
        responses = ['not json', '{}', '{"change":{}}', '{"change":{"id":null}}',
                     '{"change":{"id":123}}', '{"change":{"id":""}}',
                     '{"change":{"id":"not-a-uuid"}}', '{"change":{"id":[]}}']
        for response in responses:
            with self.subTest(response=response):
                result, calls = self.run_inside(response)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(c['command'] == 'forge' and c['args'][0] == 'preview' for c in calls))
                self.assertNotIn('done in ', result.stdout)

    def test_http_failure_is_not_hidden_by_a_parseable_change_response(self):
        result, calls = self.run_inside(json.dumps({'change': {'id': CHANGE_ID}}), http_exit=22)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(c['command'] == 'forge' and c['args'][0] == 'preview' for c in calls))

    def test_a_preview_with_the_wrong_page_is_not_a_passing_install(self):
        result, calls = self.run_inside(json.dumps({'change': {'id': CHANGE_ID}}), page='another page')
        self.assertNotEqual(result.returncode, 0)
        self.assert_preview(calls)
        self.assertIn('the preview did not serve the page', result.stdout)


if __name__ == "__main__":
    unittest.main()
