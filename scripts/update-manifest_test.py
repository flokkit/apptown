import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('manifest',Path(__file__).with_name('update-manifest.py'))
manifest=importlib.util.module_from_spec(spec)
spec.loader.exec_module(manifest)


class Publication(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        self.images=self.root/'images';self.images.mkdir()
        self.tag='v1.1.0';self.commit='1'*40
        self.facts={'sourceSchema':{'min':167,'max':177},'sourceLayout':{'min':1,'max':2},
                    'schemaResult':177,'layoutResult':2,'cliProtocol':1,
                    'daemonWorkerProtocol':2,'workerProtocol':{'min':1,'max':2}}
        self.index={'manifests':[{'platform':{'os':'linux','architecture':arch},'digest':'sha256:'+digest*64} for arch,digest in [('amd64','2'),('arm64','3')]]}
        for name in manifest.IMAGES:
            row=manifest.image_facts(self.tag,self.commit,name,'sha256:'+'4'*64,self.index)
            (self.images/(name+'.json')).write_text(json.dumps(row))
        (self.root/'RELEASE-NOTES.md').write_text('Release notes\n')
        for platform in manifest.PLATFORMS:
            self.archive(platform)

    def archive(self, platform, change=None, extra=None):
        base='apptown-'+self.tag[1:]+'-'+platform.replace('/','-')
        arch=platform.split('/')[1]
        files={n:(n+platform).encode() for n in ('bin/apptownd','bin/apptown-worker','bin/apptown')}
        files.update({'bin/apptown-linux':('linux-cli-'+arch).encode(),
                      'share/apptown/updates/stock.toml':b'stock',
                      'deploy/apptown-agent/Dockerfile':b'FROM pinned',
                      'deploy/systemd/apptown.service':b'service'})
        if platform.startswith('linux/'):
            files['bin/apptown']=files['bin/apptown-linux']
        if change: files.update(change)
        with tarfile.open(self.root/(base+'.tar.gz'),'w:gz') as tar:
            for name,data in files.items():
                if data is None: continue
                entry=tarfile.TarInfo(base+'/'+name);entry.size=len(data)
                tar.addfile(entry,io.BytesIO(data))
            if extra:
                entry=tarfile.TarInfo(base+'/'+extra);entry.size=1
                tar.addfile(entry,io.BytesIO(b'x'))

    def build(self):
        return manifest.generate(self.tag,self.commit,self.root,self.images,self.facts)

    def test_complete_metadata_is_bound_to_actual_archive_bytes(self):
        result=self.build()
        self.assertEqual(result['format'],2)
        self.assertEqual(set(result['archives']),set(manifest.PLATFORMS))
        self.assertEqual(set(result['compatibility']),set(self.facts))
        for platform,row in result['archives'].items():
            # Name, digest and size only: the manifest is read to notify, not to install.
            self.assertEqual(set(row),{'name','sha256','size'})
            self.assertEqual(row['sha256'],hashlib.sha256((self.root/row['name']).read_bytes()).hexdigest())
        sums=(self.root/'SHA256SUMS').read_text().splitlines()
        self.assertEqual(len(sums),6)
        for line in sums:
            digest,name=line.split('  ')
            self.assertEqual(digest,hashlib.sha256((self.root/name).read_bytes()).hexdigest())
        self.assertEqual(json.loads((self.root/'UPDATE-MANIFEST.json').read_text()),result)

    def test_archive_without_the_daemon_cannot_be_published(self):
        self.archive('linux/amd64', {'bin/apptownd':None})
        with self.assertRaisesRegex(ValueError, 'paired release assets'): self.build()

    def test_duplicate_or_missing_image_platform_refuses(self):
        for index in ({'manifests':self.index['manifests'][:1]}, {'manifests':self.index['manifests']+self.index['manifests'][:1]}):
            with self.assertRaises(ValueError):
                manifest.image_facts(self.tag,self.commit,'apptown','sha256:'+'4'*64,index)

    def test_foreign_release_image_cannot_be_published(self):
        path=self.images/'apptown.json';row=json.loads(path.read_text());row['commit']='5'*40;path.write_text(json.dumps(row))
        with self.assertRaisesRegex(ValueError,'another release'):self.build()
        self.assertFalse((self.root/'UPDATE-MANIFEST.json').exists())

    def test_unpaired_linux_cli_is_refused(self):
        self.archive('darwin/arm64',{'bin/apptown-linux':b'another release'})
        with self.assertRaisesRegex(ValueError,'paired Linux CLI'):self.build()

    def test_stock_recipe_and_service_pairs_are_checked(self):
        for name in ('share/apptown/updates/stock.toml','deploy/apptown-agent/Dockerfile','deploy/systemd/apptown.service'):
            self.archive('darwin/amd64',{name:b'another release'})
            with self.assertRaisesRegex(ValueError,'paired stock/agent/service'):self.build()

    def test_archive_traversal_and_duplicate_member_refuse_without_extraction(self):
        for name in ('../outside','bin/apptownd','bin/../outside'):
            self.archive('linux/amd64',extra=name)
            with self.assertRaises(ValueError):self.build()
        self.assertFalse((self.root/'outside').exists())

    def test_incomplete_or_contradictory_compatibility_refuses(self):
        initial=copy.deepcopy(self.facts)
        for change in ({'updaterProtocol':0}, {'sourceSchema':{'min':178,'max':177}}):
            self.facts={**initial,**change}
            with self.assertRaises(ValueError):self.build()
        self.facts={k:v for k,v in initial.items() if k!='cliProtocol'}
        with self.assertRaisesRegex(ValueError,'incomplete'):self.build()


if __name__=='__main__':unittest.main()
