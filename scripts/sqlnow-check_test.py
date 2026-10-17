"""Regressions for scripts/sqlnow-check.go, the SQL clock ratchet; no database required."""
import json
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path


class SQLNowCheckTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="forge-sqlnow-check-")
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "sqlnow-check"
        source = Path(__file__).with_name("sqlnow-check.go")
        subprocess.run(["go", "build", "-o", str(cls.binary), str(source)], check=True)

    def setUp(self):
        self.tree = tempfile.TemporaryDirectory(prefix="forge-sqlnow-tree-")
        self.addCleanup(self.tree.cleanup)
        self.root = Path(self.tree.name)
        self.write("go.mod", "module example.com/m\n")
        # Migration 1 is applied, and frozen with what it says; it reads the
        # clock, as old migrations do.
        self.migrations(self.FIRST)
        self.write("scripts/sqlnow-baseline.json", json.dumps({
            "frozen": {"file": "internal/dbschema/schema.go", "through": 1, "digest": ""}, "packages": {}}))
        out = self.run_check("-freeze", "1")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    FIRST = """{n: 1, stmts: []string{
            `ALTER TABLE t ADD COLUMN made_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
            `UPDATE t SET made_at = now() WHERE made_at IS NULL`,
        }},"""

    def migrations(self, *elements):
        self.write("internal/dbschema/schema.go", "package dbschema\n\ntype migration struct {\n\tn     int\n\tstmts []string\n}\n\n"
                   "var migrations = []migration{\n\t" + "\n\t".join(elements) + "\n}\n")

    def write(self, rel, source):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(source))

    def baseline(self, counts, **frozen):
        b = json.loads((self.root / "scripts/sqlnow-baseline.json").read_text())
        b["packages"] = counts
        b["frozen"].update(frozen)
        self.write("scripts/sqlnow-baseline.json", json.dumps(b))

    def run_check(self, *args):
        return subprocess.run([str(self.binary), "-root", str(self.root), *args],
                              capture_output=True, text=True, timeout=120)

    def reads(self):
        """Every read the checker lists, as (file:line, what)."""
        out = self.run_check("-v")
        self.assertNotEqual(out.returncode, 2, out.stderr)
        return [tuple(line.strip().split(": ", 1)) for line in out.stdout.splitlines() if line.startswith("    ")]

    def test_a_column_default_counts(self):
        self.write("internal/a/schema.sql", """
            CREATE TABLE t (
              made_at TIMESTAMPTZ NOT NULL DEFAULT now(),
              seen_at TIMESTAMPTZ DEFAULT (CURRENT_TIMESTAMP),
              day     DATE NOT NULL DEFAULT current_date,
              left_at TIMESTAMPTZ
            );
            ALTER TABLE t ALTER COLUMN left_at SET DEFAULT Now();
        """)
        self.assertEqual(self.reads(), [
            ("internal/a/schema.sql:3", "now()"),
            ("internal/a/schema.sql:4", "current_timestamp"),
            ("internal/a/schema.sql:5", "current_date"),
            ("internal/a/schema.sql:8", "now()"),
        ])
        self.assertEqual(self.run_check().returncode, 1)

    def test_a_trigger_function_or_view_counts(self):
        self.write("internal/a/schema.sql", """
            CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $$
            BEGIN NEW.updated_at = now(); RETURN NEW; END $$;
            CREATE TRIGGER touch BEFORE UPDATE ON t FOR EACH ROW EXECUTE FUNCTION touch();
            CREATE FUNCTION stamp() RETURNS timestamptz LANGUAGE sql AS $body$ SELECT clock_timestamp() $body$;
            CREATE VIEW due AS SELECT * FROM t WHERE due_at < LOCALTIMESTAMP;
        """)
        self.assertEqual(self.reads(), [
            ("internal/a/schema.sql:3", "now()"),
            ("internal/a/schema.sql:5", "clock_timestamp()"),
            ("internal/a/schema.sql:6", "localtimestamp"),
        ])

    def test_every_other_read_of_the_clock_counts(self):
        self.write("internal/a/internal/asql/queries.sql", """
            -- name: Touch :exec
            UPDATE t SET seen_at = now() WHERE id = $1;
            -- name: Due :many
            SELECT * FROM t WHERE due_at <= clock_timestamp() OR made_at < CURRENT_TIMESTAMP
              OR day = current_date OR at > 'now'::timestamptz OR at > timestamptz 'today'
              OR at < statement_timestamp() OR at < transaction_timestamp();
        """)
        self.assertEqual(self.reads(), [
            ("internal/a/internal/asql/queries.sql:3", "now()"),
            ("internal/a/internal/asql/queries.sql:5", "clock_timestamp()"),
            ("internal/a/internal/asql/queries.sql:5", "current_timestamp"),
            ("internal/a/internal/asql/queries.sql:6", "current_date"),
            ("internal/a/internal/asql/queries.sql:6", "'now'"),
            ("internal/a/internal/asql/queries.sql:6", "'today'"),
            ("internal/a/internal/asql/queries.sql:7", "statement_timestamp()"),
            ("internal/a/internal/asql/queries.sql:7", "transaction_timestamp()"),
        ])
        self.assertEqual(self.run_check().returncode, 1)

    def test_a_time_cast_counts_in_every_spelling(self):
        self.write("internal/a/queries.sql", """
            SELECT CAST('now' AS timestamptz);
            SELECT timestamp with time zone 'now';
            SELECT TIMESTAMP 'now', 'now'::date, 'NOW'::pg_catalog.timestamptz;
            SELECT timestamp(3) without time zone 'today 12:00', time with time zone 'now UTC';
            SELECT 'tomorrow 08:00'::timestamp, CAST(' Yesterday ' AS date), timestamptz('today 09:00');
            INSERT INTO t (at) VALUES ('now');
            SELECT timestamptz 'now'::text, CAST(timestamptz 'today' AS text), date('today')::text;
            SELECT 'now'::timestamptz::text, 'now'::text::date, CAST(CAST('now' AS text) AS timestamptz);
            SELECT CAST(text 'now' AS timestamptz), timestamptz('now'::text);
            SELECT CAST('today 10:00' AS timestamp with time zone), CAST('tomorrow 10:00' AS timestamp(3));
            SELECT ('today 12:00')::timestamp, CAST(('tomorrow 08:00') AS timestamp), ('now'::text)::timestamptz;
            SELECT ((timestamptz 'today 09:00'))::text;
        """)
        self.assertEqual(self.reads(), [
            ("internal/a/queries.sql:2", "'now'"),
            ("internal/a/queries.sql:3", "'now'"),
            ("internal/a/queries.sql:4", "'now'"),
            ("internal/a/queries.sql:4", "'now'"),
            ("internal/a/queries.sql:4", "'NOW'"),
            ("internal/a/queries.sql:5", "'today 12:00'"),
            ("internal/a/queries.sql:5", "'now UTC'"),
            ("internal/a/queries.sql:6", "'tomorrow 08:00'"),
            ("internal/a/queries.sql:6", "' Yesterday '"),
            ("internal/a/queries.sql:6", "'today 09:00'"),
            ("internal/a/queries.sql:7", "'now'"),
            ("internal/a/queries.sql:8", "'now'"),
            ("internal/a/queries.sql:8", "'today'"),
            ("internal/a/queries.sql:8", "'today'"),
            ("internal/a/queries.sql:9", "'now'"),
            ("internal/a/queries.sql:9", "'now'"),
            ("internal/a/queries.sql:9", "'now'"),
            ("internal/a/queries.sql:10", "'now'"),
            ("internal/a/queries.sql:10", "'now'"),
            ("internal/a/queries.sql:11", "'today 10:00'"),
            ("internal/a/queries.sql:11", "'tomorrow 10:00'"),
            ("internal/a/queries.sql:12", "'today 12:00'"),
            ("internal/a/queries.sql:12", "'tomorrow 08:00'"),
            ("internal/a/queries.sql:12", "'now'"),
            ("internal/a/queries.sql:13", "'today 09:00'"),
        ])

    def test_a_special_value_that_is_text_is_not_a_read(self):
        self.write("internal/a/queries.sql", """
            SELECT 'now'::text, CAST('today' AS varchar), text 'tomorrow';
            SELECT 'now is the time', 'epoch'::timestamptz, 'infinity'::date;
            SELECT 'now'''::text;
            SELECT 'now'::text::varchar, CAST('today' AS text)::character varying(3), text 'now'::varchar;
            SELECT ('now'::text)::varchar, (('today 12:00'))::text;
        """)
        self.assertEqual(self.reads(), [])

    def test_escape_and_other_strings_end_where_postgresql_ends_them(self):
        self.write("internal/a/queries.sql", r"""
            SELECT E'can\'t', now();
            SELECT e'a\\', now();
            SELECT 'it''s', now();
            SELECT U&'d\0061t', B'101', X'1F', N'x', now();
            SELECT '--', '/*', '$$', now();
            SELECT "now()", t."current_timestamp" FROM t;
            SELECT 'a''', now();""")
        self.assertEqual(self.reads(), [
            ("internal/a/queries.sql:2", "now()"),
            ("internal/a/queries.sql:3", "now()"),
            ("internal/a/queries.sql:4", "now()"),
            ("internal/a/queries.sql:5", "now()"),
            ("internal/a/queries.sql:6", "now()"),
            ("internal/a/queries.sql:8", "now()"),
        ])

    def test_a_dollar_quote_is_a_value_and_a_body(self):
        self.write("internal/a/schema.sql", """
            SELECT $$now$$::timestamptz;
            CREATE FUNCTION stamp() RETURNS timestamptz LANGUAGE sql
              AS $f$SELECT CURRENT_TIMESTAMP$f$;
            CREATE FUNCTION outer_fn() RETURNS void LANGUAGE plpgsql AS $a$
            BEGIN EXECUTE $b$ UPDATE t SET at = now() $b$; END $a$;
            SELECT $x$ $$ not closed by another tag $x$, statement_timestamp();
            SELECT * FROM t WHERE id = $1 AND at < clock_timestamp();
        """)
        self.assertEqual(self.reads(), [
            ("internal/a/schema.sql:2", "'now'"),
            ("internal/a/schema.sql:4", "current_timestamp"),
            ("internal/a/schema.sql:6", "now()"),
            ("internal/a/schema.sql:7", "statement_timestamp()"),
            ("internal/a/schema.sql:8", "clock_timestamp()"),
        ])

    def test_comments_end_where_postgresql_ends_them(self):
        self.write("internal/a/queries.sql", """
            -- now() is a comment to the line's end
            /* outer /* inner now() */ still a comment: clock_timestamp() */ SELECT localtime;
            SELECT 1 -- CURRENT_DATE
            , current_time;
        """)
        self.assertEqual(self.reads(), [
            ("internal/a/queries.sql:3", "localtime"),
            ("internal/a/queries.sql:5", "current_time"),
        ])

    def test_a_frozen_migration_is_history_and_a_new_one_counts(self):
        self.assertEqual(self.reads(), [])
        self.migrations(self.FIRST, """{n: 2, stmts: []string{
            `ALTER TABLE u ADD COLUMN made_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
            `UPDATE u SET made_at = now() WHERE made_at IS NULL`,
        }},""")
        self.assertEqual(self.reads(), [("internal/dbschema/schema.go:14", "now()"), ("internal/dbschema/schema.go:15", "now()")])
        # Other SQL in the migrations' file is not history.
        self.migrations(self.FIRST)
        with open(self.root / "internal/dbschema/schema.go", "a") as f:
            f.write('\nconst sweep = "DELETE FROM t WHERE made_at < now()"\n')
        self.assertEqual(self.reads(), [("internal/dbschema/schema.go:15", "now()")])

    def test_a_frozen_migration_may_not_change(self):
        self.migrations(self.FIRST.replace("IS NULL", "IS NULL AND true"))
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("frozen migration", out.stdout)
        self.assertIn("changed", out.stdout)

    def test_freezing_needs_migrations_that_exist(self):
        self.baseline({}, through=2)
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("last migration is 1", out.stdout)
        # A file with no migrations would exempt nothing it could see.
        self.write("internal/dbschema/schema.go", "package dbschema\n")
        self.assertEqual(self.run_check().returncode, 2)

    def test_comments_prose_and_other_strings_are_not_sql_reads(self):
        self.write("internal/a/schema.sql", """
            -- stamped with now() when it lands
            /* the old code used clock_timestamp() /* nested */ here */
            INSERT INTO notes (text) VALUES ('call now() later');
        """)
        self.write("internal/a/a.go", '''
            package a

            // now() in a Go comment is no read.
            var prompt = "land it now (gates first)?"
            var name = "now"
        ''')
        self.assertEqual(self.reads(), [])

    def test_a_go_string_literal_counts_with_its_line(self):
        self.write("internal/a/a.go", '''
            package a

            const q = `SELECT id
            FROM t
            WHERE due_at < now()`
            var r = "DELETE FROM t WHERE at < CURRENT_TIMESTAMP"
        ''')
        self.assertEqual(self.reads(), [("internal/a/a.go:6", "now()"), ("internal/a/a.go:7", "current_timestamp")])

    def test_tests_helpers_testdata_and_generated_files_are_not_held(self):
        body = '''
            package {pkg}

            const q = "UPDATE t SET at = now()"
        '''
        self.write("internal/a/a_test.go", body.format(pkg="a"))
        self.write("internal/dbtest/db.go", body.format(pkg="dbtest"))
        self.write("internal/a/afake/fake.go", body.format(pkg="afake"))
        self.write("internal/a/testdata/fixture.sql", "UPDATE t SET at = now();\n")
        self.write("internal/a/gen.sql.go", "// Code generated by sqlc. DO NOT EDIT.\n" + textwrap.dedent(body.format(pkg="a")))
        self.write("internal/dbschema/schema.sql", "-- Code generated by scripts/schema-assemble.py. DO NOT EDIT.\nUPDATE t SET at = now();\n")
        self.assertEqual(self.reads(), [])

    def test_any_read_fails_and_a_count_is_refused(self):
        self.write("internal/a/queries.sql", "UPDATE t SET at = now();\n")
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("READS THE DATABASE'S CLOCK", out.stdout)
        self.assertIn("internal/a/queries.sql:1", out.stdout)
        # A count of reads, which once held a package's old ones, is refused.
        self.baseline({"internal/a": 1})
        out = self.run_check()
        self.assertEqual(out.returncode, 2, out.stdout + out.stderr)
        self.assertIn("admits no counts", out.stderr)
        # Without the read the check passes.
        self.baseline({})
        self.write("internal/a/queries.sql", "UPDATE t SET at = sqlc.arg(at);\n")
        self.assertEqual(self.run_check().returncode, 0)


if __name__ == "__main__":
    unittest.main()
