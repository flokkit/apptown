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
if name == 'pgrep': sys.exit(1) # No restored AppTown process in this fixture.
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
    def run_cleanup(self, expected=LOCK, actual=LOCK, original_status=0, **faults):
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
        source=SCRIPT.read_text()
        cleanup=source[source.index('end_rehearsal() {'):source.index('\n# owned')]
        finish=source[source.index('owned=""'):source.index('\nstep "capture')]
        program='set -euo pipefail\n'+f'tgt=fixture\nlock={shlex.quote(expected)}\nwork={shlex.quote(str(work))}\n'+cleanup+'\n'+finish+f'\nowned=1\nexit {original_status}\n'
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

    def assert_no_remote_cleanup(self, root, calls):
        remote=[c for c in calls if c['remote']]
        self.assertTrue(all(c['command']=='cat' and c['args']==[str(root/'remote/root/.lab-lock')] for c in remote),remote)
        self.assertTrue((root/'firewall').exists())
        for kind in ('container','network','volume'):
            self.assertEqual((root/(kind+'.current')).read_text(),'existing-'+kind+'\ncreated-'+kind+'\n')
            self.assertTrue((root/'remote/opt/forge-restore'/('before.'+kind)).exists())
        self.assertIn('# apptown-recovery-rehearsal',(root/'remote/etc/hosts').read_text())

    def test_success_releases_the_entire_multiword_lock(self):
        result,root,_=self.run_cleanup()
        self.assertEqual(result.returncode,0,result.stderr)
        self.assert_completed(root)

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


class RecoveryNeedsItsDestination(unittest.TestCase):
    def test_no_registry_refuses_with_the_usage_before_any_effect(self):
        with tempfile.TemporaryDirectory(prefix='apptown-recovery-registry-') as tmp:
            bindir=Path(tmp)/'bin';bindir.mkdir()
            calls=Path(tmp)/'calls'
            for name in ('ssh','mktemp'):
                p=bindir/name;p.write_text('#!/bin/sh\necho "$0" >> '+shlex.quote(str(calls))+'\nexit 99\n');p.chmod(0o755)
            env={k:v for k,v in os.environ.items() if k!='REGISTRY'}
            env['PATH']=str(bindir)+os.pathsep+env['PATH']
            result=subprocess.run(['bash',str(SCRIPT),'src','/data','tgt','app/prod'],text=True,capture_output=True,env=env,timeout=30)
            self.assertEqual(result.returncode,2,result.stderr)
            self.assertIn('REGISTRY is not set',result.stderr)
            self.assertIn('Environment: REGISTRY (required',result.stderr)
            self.assertFalse(calls.exists(),'the script acted before refusing')


class RecoveryUsesTheSourcesOwnDatabase(unittest.TestCase):
    # The rehearsal dumps, restores and queries the database the source's
    # db.env names: on an install made before 1.0 it is forge, on one made
    # since apptown. Only the names come back from the source, never the password.
    def names(self, dbenv):
        with tempfile.TemporaryDirectory(prefix='apptown-recovery-db-') as tmp:
            root = Path(tmp)
            (root/'data').mkdir()
            (root/'data'/'db.env').write_text(dbenv)
            bindir = root/'bin'; bindir.mkdir()
            ssh = bindir/'ssh'
            ssh.write_text('#!/bin/sh\nshift\nexec /bin/sh -c "$*"\n'); ssh.chmod(0o755)
            source = SCRIPT.read_text()
            lookup = source[source.index("# The source's built-in database"):source.index("# end of the source's database")]
            program = f'set -euo pipefail\nsrc=fixture srcdata={shlex.quote(str(root/"data"))}\n' + lookup + 'echo "$srcdb $dbname"\n'
            env = {**os.environ, 'PATH': str(bindir)+os.pathsep+os.environ['PATH']}
            return subprocess.run(['bash', '-s'], input=program, text=True, capture_output=True, env=env, timeout=60)

    def test_an_install_made_before_1_0_and_one_made_since(self):
        for container, name in (('forge-db', 'forge'), ('apptown-db', 'apptown')):
            with self.subTest(name=name):
                r = self.names(f'APPTOWN_DB_URL=postgres://postgres:s3cret@127.0.0.1:54321/{name}\nAPPTOWN_DB_PASSWORD=s3cret\n'
                               f'APPTOWN_DB_CONTAINER={container}\nAPPTOWN_DB_VOLUME=v\n')
                self.assertEqual(r.returncode, 0, r.stderr)
                self.assertEqual(r.stdout, f'{container} {name}\n')
                self.assertNotIn('s3cret', r.stdout + r.stderr)

    def test_every_dump_restore_and_query_uses_that_database(self):
        source = SCRIPT.read_text()
        self.assertNotRegex(source, r'-d forge\b|postgres forge\b')
        self.assertEqual(len(re.findall(r'-d \$dbname|-d "\$name"|postgres "\$dbname"|-U postgres "\$name"', source)), 7)

    def test_a_db_env_naming_no_database_stops_it(self):
        r = self.names('APPTOWN_DB_CONTAINER=apptown-db\n')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('names no built-in database container and database', r.stderr)


if __name__=='__main__':unittest.main()
