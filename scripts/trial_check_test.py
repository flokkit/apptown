"""Exercise the maintained HTTP trial command with external command stand-ins."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(os.environ.get('TRIAL_CHECK_SCRIPT', Path(__file__).with_name('trial-check.sh')))
STUB = r'''#!/usr/bin/env python3
import os,sys,json
from pathlib import Path
args=sys.argv[1:]; root=Path(os.environ['STUB_ROOT']); kind=Path(sys.argv[0]).name
with (root/'calls').open('a') as f:f.write(json.dumps([kind,*args])+'\n')
namefile=root/'name'
name=namefile.read_text() if namefile.exists() else ''
if kind=='docker':
 if args[0]=='volume':
  if args[1]=='create':
   name=args[-1];namefile.write_text(name);(root/'volume').touch();print(name)
  elif args[1]=='ls':
   if os.environ.get('INVENTORY_FAIL'):sys.exit(29)
   if (root/'volume').exists():print(name)
  elif args[1]=='rm':
   if os.environ.get('CLEANUP_FAIL'):sys.exit(31)
   (root/'volume').unlink(missing_ok=True)
 elif args[0]=='run':
  name=args[args.index('--name')+1];namefile.write_text(name)
  (root/'container').touch();(root/'volume').touch()
  if os.environ.get('RUN_FAIL'):sys.exit(37)
  print('container-id')
 elif args[0]=='ps':
  if os.environ.get('INVENTORY_FAIL'):sys.exit(29)
  if (root/'container').exists():print(name)
 elif args[0]=='rm':(root/'container').unlink(missing_ok=True)
 elif args[0]=='logs':
  print('AppTown is ready. Open http://localhost:7777/?setup=deadbeef to create the administrator.')
  print('AppTown is ready')
 elif args[0]=='inspect':print('false' if os.environ.get('STOPPED') else 'true')
 elif args[0]=='exec':
  if 'preview' in args:print('Preview is up: http://change-123.preview.localhost:7777')
  elif 'docker' in args and 'run' in args:print('1073741824\n512')
 elif args[0]=='save':print('image')
else:
 url=args[-1]
 if url.endswith('/auth/bootstrap') and os.environ.get('BOOTSTRAP_FAIL'):sys.exit(22)
 if url.endswith('/tokens'):print('{"token":"cli-secret"}')
 elif url.endswith('/setup'):print('{"agentImage":"present"}')
 elif url.endswith('/changes'):print('{"change":{"id":"00000000-0000-0000-0000-000000000001"}}')
 elif 'change-123' in url:print('Hello from the trial.')
'''


class TrialCommand(unittest.TestCase):
    def run_trial(self, **changes):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            for name in ('docker','curl'):
                p=root/name;p.write_text(STUB);p.chmod(0o755)
            env={**os.environ,'PATH':str(root)+os.pathsep+os.environ['PATH'],'STUB_ROOT':str(root),'PORT':'7777',**changes}
            result=subprocess.run(['sh',str(SCRIPT),'1.0.1-check'],env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=30)
            calls=[__import__('json').loads(x) for x in (root/'calls').read_text().splitlines()]
            retained=[p.name for p in root.iterdir() if p.name in ('container','volume')]
            return result,calls,retained

    def test_http_scenario_removes_only_labelled_owned_resources(self):
        r,calls,retained=self.run_trial()
        self.assertEqual(r.returncode,0,r.stdout)
        run=next(c for c in calls if c[:2]==['docker','run'])
        self.assertIn('APPTOWN_LOCAL_TLS=false',run)
        self.assertIn('--label',run)
        name=run[run.index('--name')+1]
        self.assertEqual(retained,[])
        self.assertIn(['docker','rm','-f',name],calls)
        self.assertIn(['docker','volume','rm',name],calls)
        self.assertNotIn('deadbeef',r.stdout)

    def test_cleanup_failure_cannot_report_success(self):
        r,_,retained=self.run_trial(CLEANUP_FAIL='1')
        self.assertNotEqual(r.returncode,0,r.stdout)
        self.assertIn('volume',retained)

    def test_partial_start_is_cleaned_and_original_failure_preserved(self):
        r,_,retained=self.run_trial(RUN_FAIL='1')
        self.assertEqual(r.returncode,37,r.stdout)
        self.assertEqual(retained,[])

    def test_cleanup_failure_preserves_existing_scenario_status(self):
        r,_,_=self.run_trial(BOOTSTRAP_FAIL='1',CLEANUP_FAIL='1')
        self.assertEqual(r.returncode,22,r.stdout)

    def test_unknown_cleanup_inventory_fails(self):
        r,_,_=self.run_trial(INVENTORY_FAIL='1')
        self.assertNotEqual(r.returncode,0,r.stdout)

    def test_failure_does_not_print_setup_credential(self):
        # The ready line is absent, while raw logs still carry a setup token.
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            for name in ('docker','curl'):
                p=root/name;p.write_text(STUB.replace('AppTown is ready','Startup incomplete'));p.chmod(0o755)
            env={**os.environ,'PATH':str(root)+os.pathsep+os.environ['PATH'],'STUB_ROOT':str(root),'STOPPED':'1'}
            r=subprocess.run(['sh',str(SCRIPT),'1.0.1-check'],env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=30)
            self.assertNotEqual(r.returncode,0)
            self.assertNotIn('deadbeef',r.stdout)
            self.assertIn('trial stopped',r.stdout)


if __name__=='__main__':
    unittest.main()
