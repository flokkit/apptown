"""Declaration parser regressions for shape-check.py; no database required."""
import json
import importlib.util
from unittest import mock
from pathlib import Path
import subprocess
import tempfile
import unittest


class AliasesTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="forge-shape-check-")
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "shape-aliases"
        source = Path(__file__).with_name("shape-aliases.go")
        subprocess.run(["go", "build", "-o", str(cls.binary), str(source)], check=True)

    def aliases(self, declarations, imports='import "flokkit.io/forge/internal/forge/access"'):
        fixture = Path(self.work.name) / "fixture.go"
        fixture.write_text(f"package forge\n{imports}\n{declarations}\n")
        out = subprocess.run([str(self.binary), "access,deployment", str(fixture)],
                             capture_output=True, text=True, check=True)
        return [item.split(": ", 1)[1] for item in json.loads(out.stdout)]

    def test_grouped_and_single_declarations(self):
        self.assertEqual(self.aliases('''
            type Principal = access.Principal
            type (
                Account = access.Account
                Resource = access.Resource
            )
            const Scope = access.ScopeInstance
            const (
                Read = access.CapProjectRead
                Write = access.CapChangeCreate
            )
            var Err = access.ErrDenied
            var (
                Catalog = access.Definitions
                Roles = access.StockRoles
            )
        '''), ["Principal = access.Principal", "Account = access.Account",
               "Resource = access.Resource", "Scope = access.ScopeInstance",
               "Read = access.CapProjectRead", "Write = access.CapChangeCreate",
               "Err = access.ErrDenied", "Catalog = access.Definitions",
               "Roles = access.StockRoles"])

    def test_assignments_comments_strings_and_defined_types_are_not_aliases(self):
        self.assertEqual(self.aliases('''
            // type Comment = access.Account
            var text = "Comment = access.Account"
            type NewAccount access.Account
            func update() {
                capability := access.CapProjectRead
                capability = access.CapChangeCreate
                var local = access.CapProjectRead
                type Local = access.Account
                _ = capability
                _ = local
            }
        '''), [])

    def test_renamed_imports_parentheses_and_parallel_values(self):
        self.assertEqual(self.aliases('''
            type Account = (auth.Account)
            var First, Second = auth.Definitions, auth.StockRoles
        ''', 'import auth "flokkit.io/forge/internal/forge/access"'),
            ["Account = auth.Account", "First = auth.Definitions", "Second = auth.StockRoles"])

    def test_selector_requires_subsystem_import(self):
        self.assertEqual(self.aliases('''
            var access = struct{ Account string }{}
            var Value = access.Account
        ''', ''), [])

    def test_malformed_go_fails_closed(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.aliases('type Broken =')


class MethodsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="forge-methods-")
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "methods"
        subprocess.run(["go", "build", "-o", str(cls.binary),
                        str(Path(__file__).with_name("shape-methods.go"))], check=True)

    def methods(self, declarations, filename="fixture.go"):
        path = Path(self.work.name) / filename
        path.write_text("package forge\n" + declarations)
        result = subprocess.run([str(self.binary), str(path)], check=True, text=True, capture_output=True)
        return [s.split(": App.")[1] for s in json.loads(result.stdout)]

    def test_pointer_value_parenthesized_and_unnamed_receivers(self):
        self.assertEqual(self.methods("""
            type App struct{}
            func (a *App) Exported() {}
            func (other App) private() {}
            func (*App) Unnamed() {}
            func (a (*App)) Parenthesized() {}
        """), ["Exported", "private", "Unnamed", "Parenthesized"])

    def test_other_receivers_functions_comments_and_calls_do_not_count(self):
        self.assertEqual(self.methods("""
            // func (a *App) Comment() {}
            const text = "func (a *App) String() {}"
            type Other struct{}
            func (a *Other) Method() {}
            func helper(a *App) { a.Method(); _ = a.Method }
            var closure = func(a *App) { a.Method() }
        """), [])

    def test_test_only_methods_do_not_count(self):
        self.assertEqual(self.methods('func (*App) Fixture() {}', "fixture_test.go"), [])

    def test_malformed_production_go_fails_closed(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.methods('func (a *App) Broken(')


class CallbacksTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "count"
        subprocess.run(["go", "build", "-o", str(cls.binary), str(Path(__file__).with_name("shape-callbacks.go"))], check=True)

    def bindings(self, body):
        path = Path(self.work.name) / "fixture.go"
        path.write_text("package forge\n" + body)
        out = subprocess.run([str(self.binary), "--", str(path)], check=True, text=True, capture_output=True)
        return json.loads(out.stdout)

    def test_equivalent_adapter_method_and_closure_count_once(self):
        for value in ['adapter{a}', 'a.Run', 'func() { a.Run() }']:
            with self.subTest(value=value):
                found = self.bindings('type App struct{}; type adapter struct{ app *App }; func(a *App) Run(){}; func(a *App) wire(){ _ = Dependencies{Execute: ' + value + '} }')
                self.assertEqual(len(found), 1)

    def test_alias_map_and_callback_binding_helper(self):
        found = self.bindings('''type App struct{}; func(a *App) Run(){}
func bind(a *App) jobs.Handler { return func(){ a.Run() } }
func(a *App) wire(){
 b:=a
 cb:=b.Run
 _=Dependencies{Execute:cb}
 runners:=map[string]Runner{"one":func(){b.Run()}, "two":bind(a)}
 _=Dependencies{Runners:runners}
} ''')
        self.assertEqual(len(found), 3)

    def test_registration_loop_and_constructor_adapter(self):
        found=self.bindings("type App struct{}; type adapter struct{app *App}; func(a *App) Run(){}; func(a *App) wire(){runners[x]=func(){a.Run()}; conformance.New(db,adapter{a})}")
        self.assertEqual(len(found), 2)

    def test_transport_immediate_calls_and_subsystem_method_are_excluded(self):
        self.assertEqual(self.bindings('''type App struct{};func(a *App) Run(){}
func(a *App) wire(){
 _=func(){a.Run()}()
 _=mcpCall{app:a}
 _=http.Server{Handler:func(){a.Run()}}
 _=Dependencies{Wait:a.Work.Wait}
} '''), [])

    def test_invalid_go_fails_closed(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.bindings('type Broken =')


class HandwrittenSQLTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("shape", Path(__file__).with_name("shape-check.py"))
        cls.shape = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.shape)

    def test_literals_count_nested_sql_and_ignore_ui_prose(self):
        items = [dict(file="internal/forge/example.go", line=1, text=text) for text in [
            "select one enabled model", "select the project's way of working", "select or change the project's workflow copy",
            "SELECT EXISTS(SELECT 1 FROM targets WHERE provider=$1)",
            "select pg_advisory_xact_lock($1)", "SELECT $1::text", "SELECT 1", "SELECT ",
            "INSERT INTO rows(name) VALUES ($1)", "UPDATE rows SET name=$1", "DELETE FROM rows WHERE id=$1",
        ]]
        self.assertEqual(len(self.shape.handwritten_items(items)), 9)

    def test_parser_ignores_comments_go_select_generated_and_tests(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "internal/forge").mkdir(parents=True)
            (root / "cmd").mkdir()
            (root / "internal/forge/query.go").write_text('''package forge
// SELECT * FROM comment
func f() { select {} }
const query = "select * from records"
''')
            (root / "internal/forge/query_test.go").write_text('package forge; const query = "SELECT * FROM fixture"')
            (root / "internal/forge/generated.go").write_text('// Code generated by sqlc. DO NOT EDIT.\npackage forge; const q = "SELECT * FROM generated"')
            result = subprocess.run(["go", "run", str(Path(__file__).with_name("schema-sql-literals.go")), str(root)],
                                    text=True, capture_output=True, check=True)
            self.assertEqual(len(self.shape.handwritten_items(json.loads(result.stdout))), 1)


class TestSQLTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("shape", Path(__file__).with_name("shape-check.py"))
        cls.shape = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.shape)

    def count(self, items, lines):
        writes, reads = self.shape.test_sql_items(items, lambda _f: lines)
        return len(writes), len(reads)

    def test_fault_selectors_and_statement_prefixes_are_not_test_sql(self):
        # A fault injector is handed the TEXT of a statement to fail, and a
        # selector list names statements without running them. Neither is SQL
        # the test executes.
        items = [
            dict(file="a_test.go", line=1, text="INSERT INTO jobs", call="failNextStatement"),
            dict(file="a_test.go", line=1, text="INSERT INTO jobs"),
            dict(file="a_test.go", line=1, text="UPDATE changes SET state="),
            dict(file="a_test.go", line=1, text="INSERT INTO jobs(id) VALUES ($1)"),
            dict(file="a_test.go", line=1, text="UPDATE changes SET state='open'"),
            dict(file="a_test.go", line=1, text="DELETE FROM jobs WHERE id=$1"),
        ]
        self.assertEqual(self.count(items, [""]), (3, 0))

    def test_a_marker_declares_its_own_statement_only(self):
        # A wider window would let one marker silence the statement after it.
        lines = ["// sql-guarantee: malformed row no operation can produce", "first()", "second()"]
        marked = dict(file="a_test.go", line=2, text="UPDATE runs SET definition_json='{}'")
        neighbour = dict(file="a_test.go", line=3, text="UPDATE runs SET status='done'")
        self.assertEqual(self.count([marked], lines), (0, 0))
        self.assertEqual(self.count([neighbour], lines), (1, 0))
        self.assertEqual(self.count([marked, neighbour], lines), (1, 0))

    def test_reads_are_counted_apart_from_writes(self):
        items = [dict(file="a_test.go", line=1, text="SELECT status FROM runs WHERE id=$1"),
                 dict(file="a_test.go", line=1, text="select the project's way of working")]
        self.assertEqual(self.count(items, [""]), (0, 1))

    def test_exempt_owners_are_not_counted(self):
        item = dict(file="internal/dbtest/suite_test.go", line=1, text="INSERT INTO t(a) VALUES ($1)")
        self.assertEqual(self.count([item], [""]), (0, 0))


class TestHomesTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("shape", Path(__file__).with_name("shape-check.py"))
        cls.shape = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.shape)

    def count(self, files):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "internal/forge").mkdir(parents=True)
            for name, body in files.items():
                (root / "internal/forge" / name).write_text(body)
            with mock.patch.object(self.shape, "ROOT", str(root)):
                return len(self.shape.testhomes())

    def test_a_test_beside_its_production_file_is_at_home(self):
        self.assertEqual(self.count({"scan.go": "package forge", "scan_test.go": "package forge"}), 0)

    def test_a_composition_test_stays_in_root(self):
        # Assembling the App is what a root test is FOR; it is not misplaced.
        self.assertEqual(self.count({"boot_test.go": "package forge\nfunc f() { a := newTestApp(t) }"}), 0)

    def test_an_orphan_touching_one_owner_is_counted(self):
        body = ('package forge\nimport "flokkit.io/forge/internal/forge/identity"\n'
                'func TestAdmission(t *testing.T) { _ = identity.Service{} }')
        self.assertEqual(self.count({"account_test.go": body}), 1)

    def test_a_fixture_file_declaring_no_tests_is_not_counted(self):
        # Fixtures belong beside the tests that consume them.
        body = ('package forge\nimport "flokkit.io/forge/internal/forge/identity"\n'
                'func plainAccount() identity.Service { return identity.Service{} }')
        self.assertEqual(self.count({"account_fixture_test.go": body}), 0)

    def test_an_orphan_spanning_owners_is_not_counted(self):
        body = ('package forge\nimport (\n"flokkit.io/forge/internal/forge/identity"\n'
                '"flokkit.io/forge/internal/forge/project"\n)\n'
                'func TestCross(t *testing.T) { _ = identity.Service{} }')
        self.assertEqual(self.count({"cross_test.go": body}), 0)


class HarnessImportsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("shape", Path(__file__).with_name("shape-check.py"))
        cls.shape = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.shape)

    def test_composition_root_and_orchestration_owners_may_know_a_harness(self):
        base = self.shape.DOMAIN
        pi = base + "agentharness/pi"
        imports = {
            self.shape.APP: [pi],
            base + "workflowexec": [pi],
            base + "conversation": [pi],
            base + "agentharness/pi/internal/render": [pi],
            base + "workflow": [base + "agentharnessx"],  # exact path boundary, like transport()
        }
        with mock.patch.object(self.shape, "inventory", return_value=imports):
            self.assertEqual(self.shape.harness_imports(), [])

    def test_a_domain_owner_or_library_knowing_a_harness_is_counted(self):
        # The 17 September case: inference rendered Pi's models.json and no gate saw it.
        base = self.shape.DOMAIN
        pi = base + "agentharness/pi"
        for caller in [base + "inference", base + "workflow", base + "projectlifecycle", self.shape.MOD + "console"]:
            with self.subTest(caller=caller):
                with mock.patch.object(self.shape, "inventory", return_value={caller: [pi]}):
                    found = self.shape.harness_imports()
                    self.assertEqual(len(found), 1)
                    self.assertIn(caller, found[0])


class DoorsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("shape", Path(__file__).with_name("shape-check.py"))
        cls.shape = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.shape)

    def test_allowed_two_hop_path_is_not_a_direct_import(self):
        imports = {"workflow": {"adoption"}, "adoption": {"wow"}}
        with mock.patch.object(self.shape, "go_imports", side_effect=lambda p: imports.get(p, set())), \
             mock.patch.object(self.shape, "forge_importers", return_value=[]), \
             mock.patch.object(self.shape.os.path, "isdir", return_value=True):
            self.assertEqual(self.shape.doors(), [])
            imports["workflow"].add("wow")
            self.assertEqual(self.shape.doors(), ["workflow -> wow (no arrow in architecture.md §2)"])

    def test_kernels_allow_consumers_but_refuse_dependencies_back(self):
        imports = {"workflow": {"identity", "audit"}, "identity": {"audit", "dbschema"}}
        with mock.patch.object(self.shape, "go_imports", side_effect=lambda p: imports.get(p, set())), \
             mock.patch.object(self.shape, "forge_importers", return_value=[]), \
             mock.patch.object(self.shape.os.path, "isdir", return_value=True):
            self.assertEqual(self.shape.doors(), [])
            for target in ("project", "shipping", "dependency", "unknown"):
                imports["identity"] = {target}
                self.assertEqual(len(self.shape.doors()), 1, target)

    def test_direct_import_inventory_and_broken_package_refusal(self):
        with mock.patch.object(self.shape.subprocess, "run", return_value=subprocess.CompletedProcess([], 0,
                "flokkit.io/forge/internal/adoption\ncontext\n", "")) as run:
            self.assertEqual(self.shape.go_imports("workflow"), {"adoption"})
            self.assertNotIn("-deps", run.call_args.args[0])
        with mock.patch.object(self.shape.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "", "broken")):
            with self.assertRaises(RuntimeError):
                self.shape.go_imports("workflow")

    def test_app_only_retains_effect_once_guards(self):
        with tempfile.TemporaryDirectory() as directory:
            app = Path(directory) / "app.go"
            app.write_text("package forge\ntype App struct {\nconsoleStopOnce sync.Once\nconsoleCLIOnce sync.Once\nhealthOnce sync.Once\n}\n")
            with mock.patch.object(self.shape, "forge_files", return_value=[str(app)]):
                self.assertEqual(self.shape.constructors(), ["internal/forge/app.go: App.healthOnce lazily constructs state"])

    def test_unnamed_helper_cannot_import_the_app(self):
        output = "flokkit.io/forge/internal/forge/helper flokkit.io/forge/internal/forge context\n"
        with mock.patch.object(self.shape.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, output, "")):
            self.assertEqual(self.shape.forge_importers(), ["flokkit.io/forge/internal/forge/helper"])

    def test_transport_accepts_composition_and_internal_helpers_only(self):
        base = self.shape.DOMAIN
        imports = {
            self.shape.APP: [base + "httpapi"],
            base + "httpapi": [base + "workflow", base + "httpapi/internal/replies"],
            base + "httpapi/internal/replies": [base + "httpapi"],
            base + "workflow": [base + "httpapix"],  # exact path boundary
        }
        with mock.patch.object(self.shape, "inventory", return_value=imports):
            self.assertEqual(self.shape.transport(), [])
            for caller in [base + "workflow", base + "shipping", base + "unnamedhelper",
                           self.shape.MOD + "console"]:
                with self.subTest(caller=caller):
                    imports[caller] = [base + "httpapi", base + "httpapi/internal/replies"]
                    violations = self.shape.transport()
                    self.assertEqual(len(violations), 2)
                    self.assertTrue(all(v.startswith(caller + " imports ") for v in violations))
                    imports[caller] = []

    def test_mcp_transport_is_independent_of_http_and_domains(self):
        base = self.shape.DOMAIN
        imports = {self.shape.APP: [base + "mcpapi"],
                   base + "mcpapi": [base + "workflow", base + "mcpapi/internal/replies"]}
        with mock.patch.object(self.shape, "inventory", return_value=imports):
            self.assertEqual(self.shape.transport(), [])
            for caller, dependency in [(base + "workflow", base + "mcpapi"),
                                       (base + "httpapi", base + "mcpapi"),
                                       (base + "mcpapi", base + "httpapi"),
                                       (base + "helper", base + "mcpapi/internal/replies")]:
                original = imports.get(caller)
                imports[caller] = [dependency]
                self.assertEqual(len(self.shape.transport()), 1)
                if original is None:
                    del imports[caller]
                else:
                    imports[caller] = original

    def test_transport_test_imports_cannot_bypass_the_boundary(self):
        output = self.shape.DOMAIN + "workflow " + self.shape.DOMAIN + "httpapi\n"
        with mock.patch.object(self.shape.subprocess, "run",
                               return_value=subprocess.CompletedProcess([], 0, output, "")) as run:
            self.assertEqual(len(self.shape.transport()), 1)
            template = run.call_args.args[0][3]
            self.assertIn(".TestImports", template)
            self.assertIn(".XTestImports", template)

    def test_transport_cannot_import_root_forge(self):
        with mock.patch.object(self.shape, "inventory", return_value={self.shape.DOMAIN + "httpapi": [self.shape.APP]}):
            self.assertEqual(self.shape.forge_importers(), [self.shape.DOMAIN + "httpapi"])

    def test_a_library_cannot_import_forge(self):
        output = ("flokkit.io/forge/internal/console flokkit.io/forge/internal/forge/workflow context\n"
                  "flokkit.io/forge/internal/forge/workflow flokkit.io/forge/internal/console\n"
                  "flokkit.io/forge/internal/forge/access flokkit.io/forge/internal/forge\n")
        with mock.patch.object(self.shape.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, output, "")):
            self.assertEqual(self.shape.layering(), [
                "flokkit.io/forge/internal/console imports flokkit.io/forge/internal/forge/workflow (a library knows nothing of forge)"])
            self.assertEqual(self.shape.top("flokkit.io/forge/internal/forge/deployment/docker"), "deployment")
            self.assertEqual(self.shape.top("flokkit.io/forge/internal/forge"), "forge")
            self.assertEqual(self.shape.top("flokkit.io/forge/internal/console/pi"), "console")


class AgentBoundaryTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="forge-agent-boundaries-")
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "shape-agents"
        subprocess.run(["go", "build", "-o", str(cls.binary),
                        str(Path(__file__).with_name("shape-agents.go"))], check=True)

    def check(self, package, source):
        path = Path(self.work.name) / package / "fixture.go"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("package fixture\n" + source)
        result = subprocess.run([str(self.binary), "--", str(path)],
                                capture_output=True, text=True, check=True)
        return json.loads(result.stdout)

    def test_shared_policy_and_public_harness_inputs_are_allowed(self):
        self.assertEqual(self.check("agentenv", 'const prompt = "Describe the pipeline"'),
                         {"policy": [], "settlement": []})
        self.assertEqual(self.check("agentharness/pi", 'import "encoding/json"'),
                         {"policy": [], "settlement": []})

    def test_aliased_and_dot_imports_cannot_hide_dependencies(self):
        for alias in ("adapter", "."):
            source = f'import {alias} "flokkit.io/forge/internal/forge/agentharness/pi"'
            self.assertEqual(len(self.check("agentenv", source)["policy"]), 1)
            source = f'import {alias} "flokkit.io/forge/internal/forge/workflow"'
            self.assertEqual(len(self.check("agentharness/pi", source)["settlement"]), 1)

    def test_domain_facade_cannot_hide_settlement(self):
        source = 'import "flokkit.io/forge/internal/forge/projectlifecycle"'
        self.assertEqual(len(self.check("agentharness/pi", source)["settlement"]), 1)

    def test_local_settlement_interface_is_rejected(self):
        source = 'type Task interface { Finish(Outcome) error }; type Outcome struct{}'
        self.assertEqual(len(self.check("agentharness/pi", source)["settlement"]), 1)

    def test_pi_artifacts_are_forbidden_in_policy_but_allowed_in_adapter(self):
        source = 'const path = ".pi/agent/settings.json"'
        self.assertEqual(len(self.check("agentenv", source)["policy"]), 1)
        self.assertEqual(self.check("agentharness/pi", source),
                         {"policy": [], "settlement": []})

    def test_comments_are_not_configuration(self):
        self.assertEqual(self.check("agentenv", '// Pi writes settings.json; this package does not.'),
                         {"policy": [], "settlement": []})

    def test_malformed_source_fails_closed(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.check("agentenv", 'func broken(')


class RequestBoundariesTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="forge-request-boundaries-")
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "shape-requests"
        subprocess.run(["go", "build", "-o", str(cls.binary),
                        str(Path(__file__).with_name("shape-request-boundaries.go"))], check=True)

    def check(self, source, filename="fixture.go"):
        path = Path(self.work.name) / filename
        path.write_text(source)
        result = subprocess.run([str(self.binary), str(path)], capture_output=True, text=True, check=True)
        return json.loads(result.stdout)

    def test_direct_request_body_reads_probe_to_one(self):
        result = self.check('package httpapi; import "net/http"; func f(r *http.Request) { consume(r.Body) }')
        self.assertEqual(len(result["jsondecode"]), 1)

    def test_report_body_is_not_a_request(self):
        result = self.check('package httpapi; type Report struct { Body string }; func f(r Report) { consume(r.Body) }')
        self.assertEqual(result["jsondecode"], [])

    def test_renamed_decoder_and_old_helper_are_detected(self):
        self.assertEqual(len(self.check('package httpapi; func f() { codec.NewDecoder(reader) }')["jsondecode"]), 1)
        self.assertEqual(len(self.check('package httpapi; func f() { ReadJSON(r, &in) }')["jsondecode"]), 1)

    def test_raw_registration_probe_and_constructor(self):
        self.assertEqual(len(self.check('package forge; var rows = []routeSpec{{}}')["routepolicies"]), 1)
        self.assertEqual(self.check('package forge; var rows = []routeSpec{requireRoute()}')["routepolicies"], [])
        self.assertEqual(len(self.check('package mcpapi; var c = toolCommand{}')["routepolicies"]), 1)

    def test_comments_and_strings_do_not_count(self):
        self.assertEqual(self.check('package httpapi; const s = "ReadJSON(r, &in)" // r.Body'),
                         {"jsondecode": [], "routepolicies": []})


class RootSizeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("shape", Path(__file__).with_name("shape-check.py"))
        cls.shape = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.shape)

    def test_counts_only_the_root_package_split_by_tests(self):
        # A subsystem's files are the subsystem's; only internal/forge itself is the root.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "internal/forge/workflow").mkdir(parents=True)
            for name in ["app.go", "route_table.go", "app_test.go", "README.md"]:
                (root / "internal/forge" / name).write_text("package forge")
            (root / "internal/forge/workflow/run.go").write_text("package workflow")
            (root / "internal/forge/workflow/run_test.go").write_text("package workflow")
            self.assertEqual(self.shape.root_files(str(root)),
                             ["internal/forge/app.go", "internal/forge/route_table.go"])
            self.assertEqual(self.shape.root_files(str(root), tests=True), ["internal/forge/app_test.go"])

    def test_growth_points_at_the_layout_guide(self):
        for rule in ("rootfiles", "roottests"):
            self.assertIn("design/architecture/layout.md", self.shape.HINTS[rule])


class RatchetsTest(unittest.TestCase):
    """The transport, hub-path, kind-branch and history-comment counts."""
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("shape", Path(__file__).with_name("shape-check.py"))
        cls.shape = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.shape)

    def tree(self, files):
        directory = tempfile.TemporaryDirectory(prefix="forge-shape-ratchets-")
        self.addCleanup(directory.cleanup)
        for name, body in files.items():
            path = Path(directory.name) / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(body)
        return directory.name

    def test_transport_audit_counts_root_and_transport_calls_not_owners_tests_or_comments(self):
        call = "package x\nfunc f() {\n\taudit.BestEffort(db, a, 0, k, s, nil)\n\t_, _ = audit.Recent(ctx, db, f)\n}\n"
        root = self.tree({"internal/forge/app.go": call, "internal/forge/httpapi/h.go": call,
                          "internal/forge/mcpapi/m.go": call, "internal/forge/workflow/w.go": call,
                          "internal/forge/httpapi/h_test.go": call,
                          "internal/forge/httpapi/c.go": "package x\n// audit.BestEffort(db) is gone\n"})
        self.assertEqual(len(self.shape.transport_audit(root)), 6)

    def test_transport_db_counts_constructor_parameters_and_dependency_fields(self):
        root = self.tree({
            "internal/forge/httpapi/a.go": "package httpapi\ntype A struct{ db *database.DB }\n"
                                          "func NewA(work *workflow.Service,\n\tdb *database.DB) A { return A{db: db} }\n"
                                          "func helper(db database.Queryer) {}\n",
            "internal/forge/mcpapi/b.go": "package mcpapi\ntype BDependencies struct {\n\tDB *database.DB\n\tWork *workflow.Service\n}\n"
                                         "func NewB(d BDependencies, q database.Queryer) {}\n",
            "internal/forge/workflow/c.go": "package workflow\nfunc NewC(db *database.DB) {}\n"})
        found = self.shape.transport_db(root)
        self.assertEqual(len(found), 3, found)
        self.assertTrue(any("NewA" in f for f in found) and any("NewB" in f for f in found))
        self.assertTrue(any("dependency field" in f for f in found))

    def test_hub_path_is_counted_outside_the_project_owner(self):
        line = 'package x\nvar p = filepath.Join(dir, "workspaces", "project-"+slug)\n'
        root = self.tree({"internal/forge/project/paths.go": line, "internal/forge/httpapi/v.go": line,
                          "internal/forge/workflow/s.go": line, "internal/forge/workflow/s_test.go": line,
                          "internal/forge/workflow/c.go": 'package x\nvar c = filepath.Join(dir, "workspaces", "change-1")\n'})
        self.assertEqual(len(self.shape.hub_paths(root)), 2)

    def test_kind_branches_count_comparisons_in_deployment_only(self):
        body = "package x\nfunc f() {\n\tif t.Kind == KindDOApp || t.Kind != KindS3Bucket {}\n\t// t.Kind == KindDockerHost\n}\n"
        root = self.tree({"internal/forge/deployment/a.go": body, "internal/forge/httpapi/a.go": body,
                          "internal/forge/deployment/a_test.go": body})
        self.assertEqual(len(self.shape.kind_branches(root)), 2)

    def test_dropped_decodes_count_production_json_and_xml_only(self):
        body = ("package x\n"
                "func f() {\n"
                "\t_ = json.Unmarshal(raw, &v)\n"
                "\t_, _ = xml.Unmarshal(raw, &v), 1\n"
                "\t_ = json.NewDecoder(r.Body).Decode(&v)\n"
                "\tif err := json.Unmarshal(raw, &v); err != nil {}\n"
                "\t// _ = json.Unmarshal(raw, &v)\n"
                "}\n")
        root = self.tree({"internal/forge/a/a.go": body, "cmd/forge/b.go": body,
                          "internal/forge/a/a_test.go": body, "internal/forge/a/internal/asql/queries.sql.go": body})
        self.assertEqual(len(self.shape.dropped_decodes(root)), 6)

    def test_dropped_effects_count_revocations_provider_calls_and_record_writes(self):
        body = ("package x\n"
                "func f() {\n"
                "\t_ = gl.Do(ctx, \"DELETE\", path, nil, nil)\n"
                "\t_ = s.recordRestoredCheck(ctx, id)\n"
                "\t_ = t.record(ctx, \"writesStopped\", true)\n"
                "\t_, _ = protectionsql.New(s.db.SQL()).NoteRestoredCopyLeft(ctx, p)\n"
                "\t_, _, _ = gitlabJSON(\"DELETE\", url, tok, nil)\n"
                "\t_ = sess.persist()\n"
                "\t_ = f.Close()\n"
                "\t_ = os.MkdirAll(dir, 0o755)\n"
                "\tif err := gl.Do(ctx, \"GET\", p, nil, nil); err != nil {}\n"
                "}\n")
        root = self.tree({"internal/forge/a/a.go": body, "internal/forge/a/a_test.go": body})
        self.assertEqual(len(self.shape.dropped_decodes(root)), 6)

    def test_history_comments_count_provenance_not_strings_or_tests(self):
        body = ("package x\n"
                "// The lease is fenced (the reviewer's review of step 3).\n"
                "// Settled in round 3.\n"
                "// Fixed after finding S2-01.\n"
                "// Probed on 8 October 2026 (RTB-01).\n"
                "x := 1 // 2026-10-01: changed\n"
                "// See design/plans/thing.md.\n"
                "// A landing reads only; it publishes nothing.\n"
                'var u = "https://example.com/round 3"\n')
        root = self.tree({"internal/forge/workflow/a.go": body, "cmd/forge/b.go": body,
                          "internal/forge/workflow/a_test.go": body})
        self.assertEqual(len(self.shape.history_comments(root)), 12)


    def test_executable_writes_in_tests_are_refused_and_fixtures_counted(self):
        body = ("package x\n"
                "func f() {\n"
                "\tos.WriteFile(p, []byte(\"#!/bin/sh\\nexit 0\\n\"), 0o755)\n"
                "\tos.WriteFile(p, []byte(`#!/bin/sh\n(echo) \"x\"\n`),\n\t\t0700)\n"
                "\tos.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o750)\n"
                "\tos.WriteFile(p, []byte(\"data\"), 0o644)\n"
                "\tos.WriteFile(p, body, mode)\n"
                "\t// os.WriteFile(p, b, 0o755)\n"
                "\texecfile.Write(p, b, 0o755)\n"
                "\tos.WriteFile(\n\t\tp,\n\t\tb,\n\t\t0o755,\n\t)\n"
                "\tos.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o700 /* fake */)\n"
                "\tos.WriteFile(p, []byte(\"#!/bin/sh\\necho https://example.test/r\\nsed 's/a//g'\\n\"), 0o755) // a URL\n"
                "}\n")
        root = self.tree({"internal/worker/a_test.go": body, "internal/gitexec/gittest/fixture.go": body,
                          "internal/worker/a.go": body, "cmd/forge/b_test.go": body})
        writes = self.shape.test_exec_writes(root)
        self.assertEqual(len(writes), 18, writes)
        self.assertTrue(any(":4: os.WriteFile with mode 0700" in w for w in writes), writes)
        self.assertEqual(len(self.shape.test_exec_fixtures(root)), 3)

class FacadeAllowanceTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("shape", Path(__file__).with_name("shape-check.py"))
        cls.shape = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.shape)

    def doors(self, imports):
        with mock.patch.object(self.shape, "go_imports", side_effect=lambda p: imports.get(p, set())), \
             mock.patch.object(self.shape, "forge_importers", return_value=[]), \
             mock.patch.object(self.shape.os.path, "isdir", return_value=True):
            return self.shape.doors()

    def test_a_facade_may_import_only_its_allowance_leaves_kernels_and_libraries(self):
        allowed = set(self.shape.ALLOWED["modelcall"])
        self.assertEqual(self.doors({"modelcall": allowed | {"audit", "database", "strs"}}), [])
        self.assertEqual(self.doors({"modelcall": allowed | {"workflow"}}),
                         ["modelcall -> workflow (not in the facade's import allowance)"])
        self.assertEqual(len(self.doors({"modelcall": {"repositoryedit"}})), 1)

    def test_every_facade_has_an_allowance(self):
        self.assertEqual([f for f in self.shape.FACADES if f not in self.shape.ALLOWED], [])


if __name__ == "__main__":
    unittest.main()
