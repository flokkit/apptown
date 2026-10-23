"""Fixture regressions for shape-report.py, one per report; no real Go module
or `go` binary required (app_reach_per_package mocks subprocess.run, in the
style of shape-check_test.py's DoorsTest)."""
import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

spec = importlib.util.spec_from_file_location("shape_report", Path(__file__).with_name("shape-report.py"))
shape = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shape)


class ShapeReportTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix="apptown-shape-report-")
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        old_root = shape.ROOT
        shape.ROOT = str(self.root)
        self.addCleanup(setattr, shape, "ROOT", old_root)

    def w(self, relpath, content):
        p = self.root / relpath
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content)

    def test_tables_written_from_many_packages(self):
        self.w("internal/apptown/workflow/landing.go", """package workflow
func f() { tx.Exec(`UPDATE workflow_landings SET x=1 WHERE id=$1`, 1) }
""")
        self.w("internal/apptown/remediation/budget.go", """package remediation
func g() { tx.Exec(`UPDATE workflow_landings SET y=2 WHERE id=$1`, 1) }
""")
        self.w("internal/apptown/access/foo.go", """package access
func h() { tx.Exec(`INSERT INTO solo_table(a) VALUES(1)`) }
""")
        # a sqlc directory nested under a subsystem's own internal/ belongs to
        # that subsystem, not to a package named after the sqlc directory.
        self.w("internal/apptown/access/internal/accesssql/queries.sql",
               "-- name: Insert :exec\nINSERT INTO shared_rows(a) VALUES ($1);\n")
        self.w("internal/apptown/deployment/shared.go", """package deployment
func f() { tx.Exec(`UPDATE shared_rows SET a=1`) }
""")
        # an owner that keeps its named SQL as several generated files.
        self.w("internal/apptown/scm/internal/scmsql/merges.sql",
               "-- name: Merge :exec\nUPDATE split_rows SET a=1;\n")
        self.w("internal/apptown/scm/internal/scmsql/merges.sql.go", "package scmsql\n")
        self.w("internal/apptown/preview/split.go", """package preview
func f() { tx.Exec(`DELETE FROM split_rows`) }
""")
        found = {it["table"]: set(it["packages"]) for it in shape.tables_written_from_many_packages()}
        self.assertEqual(found.get("split_rows"), {"scm", "preview"})
        self.assertEqual(found.get("workflow_landings"), {"workflow", "remediation"})
        self.assertEqual(found.get("shared_rows"), {"access", "deployment"})
        self.assertNotIn("solo_table", found)

    def test_app_fields_that_are_parts(self):
        self.w("internal/apptown/foo/service.go", """package foo
type Service struct{}
type Part struct{}
""")
        self.w("internal/apptown/store.go", "package apptown\ntype store struct{ db *sql.DB }\n")
        self.w("internal/apptown/app.go", """package apptown
type App struct {
	Foo         *foo.Part
	guardedOnce sync.Once
	guarded     *int
	Store       store
}
""")
        items = shape.app_fields_that_are_parts()
        found = {(it["field"], it["kind"]) for it in items}
        self.assertIn(("Foo", "part-typed field"), found)
        self.assertIn(("guardedOnce", "sync.Once field"), found)
        self.assertIn(("store", "field type holds a db handle"), found)

    def test_matching_type_pairs(self):
        self.w("internal/apptown/one/a.go", """package one
type Thing struct {
	Name      string
	ID        int64
	CreatedAt string
	Kind      string
}
""")
        self.w("internal/apptown/two/b.go", """package two
type Other struct {
	Name      string
	ID        int64
	CreatedAt string
	Kind      string
}
""")
        pairs = {(it["a"], it["b"]) for it in shape.matching_type_pairs()}
        self.assertIn(("internal/apptown/one.Thing", "internal/apptown/two.Other"), pairs)

    def test_app_reach_per_package(self):
        self.w("internal/apptown/app.go", "package apptown\ntype App struct {\n\tFoo *foo.Service\n}\n")
        self.w("internal/apptown/handler.go", """package apptown
func h(a *App) {
	foo.Bar()
	a.Foo.Baz()
}
""")
        listed = subprocess.CompletedProcess([], 0, "github.com/flokkit/apptown/internal/apptown/foo\n", "")
        with mock.patch.object(shape.subprocess, "run", return_value=listed):
            items = shape.app_reach_per_package()
        # app.go's own field type *foo.Service is itself a foo.Exported reference.
        row = next(it for it in items if it["package"] == "foo")
        self.assertEqual(row["references"], 2)
        self.assertEqual(row["door_calls"], 1)

    def test_doors_with_an_actor_string(self):
        # A subsystem the report reads, by the shared list, so a package that
        # stops being a subsystem does not leave this fixture where no one looks.
        sub = shape.SUBSYSTEMS[0]
        self.w(shape.pkg_dir(sub) + "/service.go", f"""package {sub}
func (s *Service) Revoke(id int64, by string) error {{ return nil }}
func (s *Service) internalHelper(by string) {{}}
""")
        sigs = {it["signature"] for it in shape.doors_with_an_actor_string()}
        self.assertTrue(any("Revoke" in s for s in sigs))
        self.assertFalse(any("internalHelper" in s for s in sigs))


if __name__ == "__main__":
    unittest.main()
