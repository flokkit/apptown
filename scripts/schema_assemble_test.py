import importlib.util
from pathlib import Path
import json
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('assembly', Path(__file__).with_name('schema-assemble.py'))
assembly = importlib.util.module_from_spec(spec)
spec.loader.exec_module(assembly)


class AssemblyTests(unittest.TestCase):
    def fixture(self, root):
        db = root / 'internal/apptown/dbschema'
        domain = root / 'internal/apptown/project'
        db.mkdir(parents=True)
        domain.mkdir()
        paths = ['internal/apptown/dbschema/version.sql', 'internal/apptown/project/schema.sql']
        (db / 'version.sql').write_text('CREATE TABLE schema_version (version bigint);\n')
        (domain / 'schema.sql').write_text('CREATE TABLE projects (id bigint);\n')
        (root / assembly.MANIFEST).write_text(json.dumps(paths))
        (root / 'sqlc.yaml').write_text('sql:\n  - engine: postgresql\n    schema: old.sql\n    queries: queries.sql\n')
        return paths

    def test_generation_is_deterministic_and_checked(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            paths = self.fixture(root)
            assembly.run(root)
            first = assembly.outputs(root)
            assembly.run(root, check=True)
            assembly.run(root)
            self.assertEqual(first, assembly.outputs(root))
            bundle = (root / assembly.BUNDLE).read_text()
            self.assertLess(bundle.index('schema_version'), bundle.index('projects'))
            self.assertIn('      - ' + paths[1], (root / 'sqlc.yaml').read_text())
            (root / paths[1]).write_text('CREATE TABLE projects (id bigint PRIMARY KEY);\n')
            with self.assertRaisesRegex(ValueError, 'stale'):
                assembly.run(root, check=True)

    def test_external_query_schema_never_enters_runtime_bundle(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.fixture(root)
            external = 'internal/apptown/jobs/internal/jobsql/river-schema.sql'
            query = 'internal/apptown/jobs/internal/jobsql/queries.sql'
            path = root / external
            path.parent.mkdir(parents=True)
            path.write_text('CREATE TABLE river_job (id bigint);')
            config = root / 'sqlc.yaml'
            config.write_text(config.read_text() + '  - engine: postgresql\n    schema: stale.sql\n    queries: ' + query + '\n')
            assembly.run(root)
            self.assertIn('schema:\n      - ' + external + '\n    queries: ' + query, config.read_text())
            self.assertNotIn('river_job', (root / assembly.BUNDLE).read_text())
            self.assertNotIn('river_job', assembly.table_owners(root))
            assembly.run(root, check=True)

    def test_missing_duplicate_and_unlisted_fragments_fail(self):
        for change in ['missing', 'duplicate', 'unlisted']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as d:
                root = Path(d)
                paths = self.fixture(root)
                if change == 'missing':
                    (root / paths[1]).unlink()
                elif change == 'duplicate':
                    (root / assembly.MANIFEST).write_text(json.dumps(paths + paths[:1]))
                else:
                    extra = root / 'internal/apptown/extra'
                    extra.mkdir()
                    (extra / 'schema.sql').write_text('CREATE TABLE extra (id bigint);')
                with self.assertRaises(ValueError):
                    assembly.run(root)

    def test_stale_schema_lists_are_detected(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.fixture(root)
            assembly.run(root)
            config = root / 'sqlc.yaml'
            config.write_text(config.read_text().replace('project/schema.sql', 'wrong/schema.sql'))
            with self.assertRaisesRegex(ValueError, 'sqlc.yaml'):
                assembly.run(root, check=True)

    def test_table_owner_is_its_fragment(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.fixture(root)
            self.assertEqual(assembly.table_owners(root), {'schema_version': 'dbschema', 'projects': 'project'})
            (root / 'internal/apptown/project/schema.sql').write_text('CREATE TABLE schema_version (id bigint);')
            with self.assertRaisesRegex(ValueError, 'declared by both'):
                assembly.run(root)

    def test_unsupported_ddl_cannot_silently_drop_ownership(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.fixture(root)
            (root / 'internal/apptown/project/schema.sql').write_text('CREATE TABLE "projects" (id bigint);')
            with self.assertRaisesRegex(ValueError, 'canonical'):
                assembly.run(root)
