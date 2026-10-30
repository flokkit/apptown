"""Execute the real recovery cleanup/EXIT trap through SSH-style command joining.
All remote paths and external effects live in a temporary local sandbox."""
import json
import os
import re
import shlex
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / 'apptown-recovery-rehearsal.sh'
LOCK = 'apptown-recovery-rehearsal 123 from fixture at 2026-10-14T00:00:00Z'
STUB = r'''#!/usr/bin/env python3
import json, os, subprocess, sys
from pathlib import Path
name, args = Path(sys.argv[0]).name, sys.argv[1:]
root = Path(os.environ['FIXTURE_ROOT'])
with open(root/'calls', 'a') as f:
    f.write(json.dumps({'command':name,'args':args,'remote':os.environ.get('FIXTURE_REMOTE')=='1'})+'\n')
lock = str(root/'remote/root/.lab-lock')
if name == 'ssh':
    if os.environ.get('SSH_FAILURE') == '1': sys.exit(255)
    command = ' '.join(args[1:]) # OpenSSH sends one joined command to the shell.
    body = sys.stdin.read()
    for before, after in [('/opt/forge-restore',str(root/'remote/opt/forge-restore')),
                          ('/root/.lab-lock',lock),('/etc/hosts',str(root/'remote/etc/hosts'))]:
        command,body = command.replace(before,after),body.replace(before,after)
    env = {**os.environ,'FIXTURE_REMOTE':'1'}
    # A hang guard: parallel CI may delay these freshly written executables.
    sys.exit(subprocess.run(['/bin/sh','-c',command],input=body,text=True,env=env,timeout=120).returncode)
if name == 'cat' and args == [lock]:
    counter = root/'lock-reads'
    n = int(counter.read_text())+1 if counter.exists() else 1
    counter.write_text(str(n))
    if str(n) == os.environ.get('FAIL_LOCK_READ_AT'): sys.exit(1)
    if str(n) == os.environ.get('REPLACE_LOCK_AT'): Path(lock).write_text(os.environ['REPLACEMENT_LOCK']+'\n')
if name == 'rm' and lock in args and os.environ.get('FAIL_LOCK_REMOVE') == '1': sys.exit(1)
if name in ('cat','rm'):
    os.execv('/bin/'+name,[name]+args)
if name == 'pgrep': sys.exit(int(os.environ.get('PGREP_STATUS', '1')))
if name == 'sed':
    if args[0] == '-i':
        p=Path(args[-1]);p.write_text(''.join(x for x in p.read_text().splitlines(True) if '# apptown-recovery-rehearsal' not in x))
        sys.exit(0)
    os.execv('/usr/bin/sed',['sed']+args)
if name == 'nft':
    p=root/'firewall'
    if args[:2] == ['delete','table']: p.unlink(missing_ok=True)
    elif args[:2] == ['list','tables']:
        if p.exists():print('table inet forge_rehearsal')
    else:sys.exit(99)
    sys.exit(0)
if name == 'docker':
    kind = 'container' if args[0] == 'ps' else args[0]
    p=root/(kind+'.current')
    if args[0]=='ps' or args[1]=='ls':print(p.read_text(),end='')
    elif args[1]=='rm':p.write_text(''.join(x+'\n' for x in p.read_text().splitlines() if x not in args[2:]))
    else:sys.exit(99)
    sys.exit(0)
sys.exit('unexpected fixture command '+name)
'''


class RecoveryReleasesOnlyItsOwnLock(unittest.TestCase):
    def run_cleanup(self, expected=LOCK, actual=LOCK, original_status=0, archive=False, **faults):
        tmp = tempfile.TemporaryDirectory(prefix='apptown-recovery-lock-')
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        bindir = root/'bin';bindir.mkdir()
        for name in ('ssh','cat','rm','pgrep','sed','nft','docker'):
            p=bindir/name;p.write_text(STUB);p.chmod(0o755)
        d=root/'remote/opt/forge-restore';d.mkdir(parents=True)
        (root/'remote/root').mkdir()
        (root/'remote/etc').mkdir()
        lock=root/'remote/root/.lab-lock'
        if actual is not None:lock.write_text(actual+'\n')
        hosts=root/'remote/etc/hosts';hosts.write_text('127.0.0.1 localhost\n127.0.0.1 blocked # apptown-recovery-rehearsal\n')
        (root/'firewall').write_text('present')
        for kind in ('container','network','volume'):
            (d/('before.'+kind)).write_text('existing-'+kind+'\n')
            (root/(kind+'.current')).write_text('existing-'+kind+'\ncreated-'+kind+'\n')
        work=root/'work';work.mkdir()
        data=root/'remote/srv/data';data.mkdir(parents=True);(data/'db.env').write_text('restored\n')
        srcarchive=root/'remote/var/tmp/apptown-rehearsal-x/apptown.backup'
        if archive:srcarchive.parent.mkdir(parents=True);srcarchive.write_text('every secret\n')
        source=SCRIPT.read_text()
        cleanup=source[source.index('end_rehearsal() {'):source.index('\n# owned')]
        finish=source[source.index('owned="" locked=""'):source.index('\ntrap finish EXIT')]+'\ntrap finish EXIT'
        program=('set -euo pipefail\n'+f'tgt=fixture\nsrc=fixture-source\nsrcdir={shlex.quote(str(srcarchive.parent)) if archive else ""}\ntdata={shlex.quote(str(data))}\nlock={shlex.quote(expected)}\nwork={shlex.quote(str(work))}\n'
                 +cleanup+'\n'+finish+f'\nowned=1\nexit {original_status}\n')
        env={**os.environ,'PATH':str(bindir)+os.pathsep+os.environ['PATH'],'FIXTURE_ROOT':str(root),**faults}
        # Allow the child hang guard to report failure and the EXIT trap to finish.
        result=subprocess.run(['bash','-s'],input=program,text=True,capture_output=True,env=env,timeout=150)
        calls=[json.loads(x) for x in (root/'calls').read_text().splitlines()]
        return result,root,calls

    def assert_completed(self, root):
        self.assertFalse((root/'remote/root/.lab-lock').exists(),'full invocation lock outlived successful cleanup')
        self.assertFalse((root/'firewall').exists())
        for kind in ('container','network','volume'):
            self.assertEqual((root/(kind+'.current')).read_text(),'existing-'+kind+'\n')
            self.assertFalse((root/'remote/opt/forge-restore'/('before.'+kind)).exists())
        self.assertEqual((root/'remote/etc/hosts').read_text(),'127.0.0.1 localhost\n')
        self.assertFalse((root/'remote/srv/data').exists(),'the restored data outlived the rehearsal')

    def assert_no_remote_cleanup(self, root, calls):
        remote=[c for c in calls if c['remote']]
        self.assertTrue(all(c['command']=='cat' and c['args']==[str(root/'remote/root/.lab-lock')] for c in remote),remote)
        self.assertTrue((root/'firewall').exists())
        for kind in ('container','network','volume'):
            self.assertEqual((root/(kind+'.current')).read_text(),'existing-'+kind+'\ncreated-'+kind+'\n')
            self.assertTrue((root/'remote/opt/forge-restore'/('before.'+kind)).exists())
        self.assertIn('# apptown-recovery-rehearsal',(root/'remote/etc/hosts').read_text())

    def test_unavailable_process_inventory_preserves_resources_cut_and_lock(self):
        for status in ('2', '127'):
            with self.subTest(status=status):
                result,root,calls=self.run_cleanup(PGREP_STATUS=status)
                self.assertNotEqual(result.returncode,0,result.stdout+result.stderr)
                self.assertIn('cannot list AppTown processes',result.stderr)
                self.assertFalse(any(c['remote'] and c['command'] in ('docker','nft','rm','sed') for c in calls),calls)
                self.assertEqual((root/'remote/root/.lab-lock').read_text(),LOCK+'\n')
                self.assertTrue((root/'firewall').exists())
                for kind in ('container','network','volume'):
                    self.assertEqual((root/(kind+'.current')).read_text(),'existing-'+kind+'\ncreated-'+kind+'\n')
                    self.assertTrue((root/'remote/opt/forge-restore'/('before.'+kind)).exists())

    def test_success_releases_the_entire_multiword_lock(self):
        result,root,_=self.run_cleanup()
        self.assertEqual(result.returncode,0,result.stderr)
        self.assert_completed(root)

    def test_the_sources_archive_is_deleted_however_the_run_ends(self):
        for status in (0, 3):
            with self.subTest(status=status):
                result,root,_=self.run_cleanup(original_status=status,archive=True)
                self.assertEqual(result.returncode,status,result.stderr)
                self.assertFalse((root/'remote/var/tmp/apptown-rehearsal-x').exists())

    def test_failure_cleanup_releases_lock_but_preserves_original_failure(self):
        result,root,_=self.run_cleanup(original_status=3)
        self.assertEqual(result.returncode,3,result.stderr)
        self.assert_completed(root)

    def test_shell_punctuation_in_full_identity_survives_ssh_joining(self):
        identity=LOCK+" fixture's $(hostname) ; owned"
        result,root,_=self.run_cleanup(expected=identity,actual=identity)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assert_completed(root)

    def test_foreign_missing_or_unreadable_lock_refuses_before_cleanup_effects(self):
        for actual,faults in [('apptown-recovery-rehearsal another invocation',{}),(None,{}),(LOCK,{'FAIL_LOCK_READ_AT':'1'})]:
            with self.subTest(actual=actual,faults=faults):
                result,root,calls=self.run_cleanup(actual=actual,**faults)
                self.assertNotEqual(result.returncode,0)
                self.assert_no_remote_cleanup(root,calls)
                if actual is not None:self.assertEqual((root/'remote/root/.lab-lock').read_text(),actual+'\n')

    def test_late_read_or_removal_failure_is_not_successful_lock_release(self):
        for faults in ({'FAIL_LOCK_READ_AT':'2'},{'FAIL_LOCK_REMOVE':'1'}):
            with self.subTest(faults=faults):
                result,root,_=self.run_cleanup(**faults)
                self.assertNotEqual(result.returncode,0)
                self.assertTrue((root/'remote/root/.lab-lock').exists())
                self.assertIn('lock',result.stderr)
                self.assertNotIn('network cut and the lock stay',result.stderr)

    def test_a_changed_owner_at_final_release_keeps_the_foreign_lock(self):
        foreign='apptown-recovery-rehearsal another invocation'
        result,root,_=self.run_cleanup(REPLACE_LOCK_AT='2',REPLACEMENT_LOCK=foreign)
        self.assertNotEqual(result.returncode,0)
        self.assertEqual((root/'remote/root/.lab-lock').read_text(),foreign+'\n')
        self.assertIn('lock release was not confirmed',result.stderr)

    def test_ssh_failure_never_claims_cleanup_completed(self):
        result,root,calls=self.run_cleanup(SSH_FAILURE='1')
        self.assertNotEqual(result.returncode,0)
        self.assert_no_remote_cleanup(root,calls)


class RecoveryResetRequiresProcessInventory(unittest.TestCase):
    def test_reset_distinguishes_no_process_from_unavailable_inventory(self):
        source=SCRIPT.read_text()
        reset=source[source.index('if [ "${RESET_TARGET:-}" = 1 ]; then'):]
        program=reset.split("<<'EOF'\n",1)[1].split('\nEOF',1)[0]
        for status in (1,2,127):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp)
                calls=root/'removal'
                for name,body in {
                    'ls':'exit 1',
                    'pgrep':f'exit {status}',
                    'rm':'printf reached > '+shlex.quote(str(calls)),
                }.items():
                    stub=root/name
                    stub.write_text('#!/bin/sh\n'+body+'\n')
                    stub.chmod(0o755)
                env={**os.environ,'PATH':str(root)+os.pathsep+os.environ['PATH']}
                result=subprocess.run(['bash','-s','--','unused'],input=program,text=True,capture_output=True,env=env,timeout=30)
                self.assertEqual(result.returncode==0,status==1,result.stderr)
                self.assertEqual(calls.exists(),status==1,'reset removed files without confirming process absence')


class RecoveryHoldsTheTargetToTheSourcesKeys(unittest.TestCase):
    # Each key file the source has is on the target unchanged; a source on
    # -db-url has no db.env, and the target's own (the built-in database the
    # restore made) is not compared; AppTown's own keys must be on the source.
    def compare(self, src, tgt):
        source = SCRIPT.read_text()
        fn = source[source.index('compare_keys() {'):source.index('\n}\n', source.index('compare_keys() {')) + 3]
        with tempfile.TemporaryDirectory() as tmp:
            a, b = Path(tmp)/'src', Path(tmp)/'tgt'
            a.write_text(''.join(l + '\n' for l in src))
            b.write_text(''.join(l + '\n' for l in tgt))
            return subprocess.run(['bash', '-c', fn + f'\ncompare_keys {shlex.quote(str(a))} {shlex.quote(str(b))}'],
                                  text=True, capture_output=True, timeout=30)

    def test_an_external_database_source_passes_beside_the_targets_own_db_env(self):
        r = self.compare(['db.env missing', 'instance-id 1', 'runtime-token-key 2'],
                         ['db.env 9', 'instance-id 1', 'runtime-token-key 2'])
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_a_changed_or_lost_file_and_a_source_without_its_keys_fail(self):
        for src, tgt, said in ((['db.env 3', 'instance-id 1', 'runtime-token-key 2'], ['db.env 9', 'instance-id 1', 'runtime-token-key 2'], 'db.env'),
                               (['instance-id 1', 'runtime-token-key 2', 'ssh/host-key 4'], ['instance-id 1', 'runtime-token-key 2'], 'ssh/host-key'),
                               (['instance-id 1', 'runtime-token-key missing'], ['instance-id 1', 'runtime-token-key missing'], 'runtime-token-key')):
            with self.subTest(said=said):
                r = self.compare(src, tgt)
                self.assertNotEqual(r.returncode, 0)
                self.assertIn(said, r.stderr)


class RecoveryChecksHealthAndKeepsStoredBackupsOpenable(unittest.TestCase):
    def test_an_unhealthy_start_fails_though_it_names_the_version(self):
        from http.server import BaseHTTPRequestHandler, HTTPServer
        import threading

        class Down(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_GET(self):
                self.send_response(503)
                self.send_header('X-AppTown-Version', 'v1.2.3')
                self.end_headers()

        server = HTTPServer(('127.0.0.1', 0), Down)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        source = SCRIPT.read_text()
        block = source.split('\n\tstep "start normally:', 1)[1]
        block = '\tstep "start normally:' + block.split('\n\tstep "clean up:', 1)[0]
        with tempfile.TemporaryDirectory() as work:
            program = ('set -euo pipefail\nstep() { :; }; start() { :; }\n'
                       f'expected=v1.2.3\nwork={shlex.quote(work)}\nturl=http://127.0.0.1:{server.server_port}\n' + block)
            r = subprocess.run(['bash', '-c', program], capture_output=True, text=True, timeout=30)
        self.assertNotEqual(r.returncode, 0, r.stdout)
        self.assertIn('answers 503', r.stderr)

    def test_a_kept_backup_needs_a_kept_passphrase(self):
        with tempfile.TemporaryDirectory() as tmp:
            calls = Path(tmp)/'calls'
            ssh = Path(tmp)/'ssh'
            ssh.write_text('#!/bin/sh\necho ssh >> ' + shlex.quote(str(calls)) + '\nexit 99\n'); ssh.chmod(0o755)
            for store, said in (('1', 'PASSPHRASE_FILE'), ('true', 'STORE is 1 or unset')):
                with self.subTest(store=store):
                    env = {**os.environ, 'PATH': tmp + os.pathsep + os.environ['PATH'], 'STORE': store}
                    env.pop('PASSPHRASE_FILE', None)
                    r = subprocess.run(['bash', str(SCRIPT), 'src', '/data', 'tgt'], capture_output=True, text=True, env=env, timeout=30)
                    self.assertEqual(r.returncode, 2, r.stderr)
                    self.assertIn(said, r.stderr)
                    self.assertFalse(calls.exists(), 'the script acted before refusing')


if __name__=='__main__':unittest.main()
