#!/usr/bin/env python3
"""The shape checker (design/architecture/layout.md, design/architecture/domains.md).

Rules over the Go tree, each producing a count of violations:
  layering      a library (internal/<x>) imports forge (internal/forge/...): libraries know
                nothing of forge; forge's subsystems live under internal/forge/<x>
  doors         a subsystem imports another subsystem where the architecture draws no arrow,
                or anything imports the App (internal/forge)
  transport     only forge or the same transport may import httpapi / mcpapi
  methods       production method declarations on App (pointer or value receiver)
  fields        a subsystem's Service struct exports a field: its parts are reached
                only through operations (domains.md, principle 1)
  constructors  a subsystem's constructor (<pkg>.New...( ) is called in forge outside app.go
  aliases       forge aliases a subsystem's name (type x = pkg.X, or x = pkg.X in a const/var block)
  rootfiles     production .go files in internal/forge itself, the composition root
  roottests     _test.go files in internal/forge itself (the root went from 308 files on 20 September to 412 on 5 October with
                every other rule flat, because none of them counts what lands in root)
  transportaudit  audit.BestEffort / audit.Recent calls in the root, httpapi and mcpapi: the
                owner records its act in its own transaction
  transportdb   httpapi / mcpapi constructors (or dependency structs) taking the raw database
  hubpaths      the project repository path ("workspaces", "project-") spelled outside the
                project owner
  kindbranches  `.Kind == Kind...` comparisons in internal/forge/deployment, where the driver
                should answer
  historycomments  production comments that narrate review history (reviewer, round, finding
                id, date) or cite design/plans and design/reviews
A facade may import only the forge packages its ALLOWED entry lists.

Exit 0 when every count is at or below scripts/shape-baseline.json; exit 1 otherwise,
or when the baseline is above the counts by more than zero and --tighten was not passed
(a slice that earns a lower number records it in the same commit).
"""
import json, os, re, subprocess, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MOD = "flokkit.io/forge/internal/"
APP = MOD + "forge"     # the App, the composition root; forge's subsystems live under it
DOMAIN = APP + "/"
# Libraries sit at internal/<x>: they know nothing of forge and could be lifted into another
# program. A library may not import the App or anything under it.
LIBRARIES = {"access", "check", "clock", "console", "database", "dbtest", "deploy", "diskspace", "dockerlabel", "durable", "enum", "execfile", "execution", "fault", "gitexec", "gitlab", "hostmem", "ids", "oci", "poll", "progress", "localtls", "stamp", "strs", "telemetry", "testreport", "trailer", "wiregen", "worker"}

# A kernel is a subsystem every other subsystem may import, and which may import
# only leaves, libraries and kernels. access is no longer one: the rule and its
# vocabulary are a library now (internal/access, no tables). identity keeps the
# exception for the account and grant reads a domain preflight needs
# (RequireEnabledIn, RootIn, ApproverGroupsIn, ActiveGrantsIn) - seven functions
# to narrow, not a general licence.
# operations is the ledger of long actions: any owner
# admits its kinds there and the ledger imports no domain owner, so it is a kernel too.
KERNELS = {"audit", "identity", "operations"}
SUBSYSTEMS = ["identity", "project", "integration", "wow", "adoption", "deployment", "preview", "dataprotection", "workflow",
              "console", "conformance", "model", "secret", "scm", "adrs", "complaint",
              "dependency", "usage", "agentenv", "tasks"]
FACADES = ["changeconsole", "remediation", "projectlifecycle", "modelcall", "conversation", "workflowexec",
           "verification", "maintenance", "workcommands", "inference", "changeadds", "evals"]
# jobs is the queue the daemon builds once with every subsystem's handlers bound: a
# cross-cutting facility, not a subsystem.
LEAVES = {"database", "audit", "enum", "fault", "gitexec", "localtls", "stamp", "strs", "wiregen", "schema", "execution", "dbtest", "dbschema", "appcontract", "jobs",
          "projectsql", "releasesql", "accesssql", "librarysql", "progress", "httpx", "resources",
          "dependency"}
# The arrows between subsystems. A subsystem may import only the subsystems
# named here. A facade composes owners, so its set is wider, but it is still a
# set: each facade lists every forge package it imports beyond leaves, kernels
# and libraries (each set began as the facade's actual imports), so a
# new dependency is a visible diff here rather than something no gate sees.
ALLOWED = {
    "agentenv":    {"integration", "project"},
    "adoption":    {"wow", "integration"},
    "workflow":    {"project", "adoption", "integration", "scm", "adrs"},
    "scm":         {"project"},
    "conformance": set(),
    "model":       {"integration", "project"},
    "integration": {"project", "secret"},
    "deployment":  {"project", "secret", "integration"},
    "preview":     {"deployment", "project", "integration"},
    "dataprotection": {"deployment", "project", "integration", "secret"},
    # Facades.
    "changeconsole":    {"console", "conversation", "integration", "preview", "project", "repositoryedit", "scm",
                         "usage", "workflow"},
    "remediation":      {"adoption", "conformance", "project", "workflow"},
    "projectlifecycle": {"adoption", "adrs", "conformance", "console", "conversation", "dataprotection", "deployment", "integration",
                         "maintenance", "model", "project", "scm", "secret", "usage", "workflow", "wow"},
    "modelcall":        set(),
    "conversation":     {"adoption", "agentenv", "agentharness", "changeactions", "complaint", "console", "integration",
                         "model", "project", "scm", "usage", "workflow", "wow"},
    "workflowexec":     {"adoption", "agentenv", "agentharness", "changeactions", "complaint", "console", "deployment",
                         "integration", "model", "preview", "project", "repositoryedit", "scm", "secret", "tasks", "usage",
                         "verification", "workflow", "wow"},
    "verification":     {"adoption", "agentenv", "conformance", "deployment", "integration", "model", "modelcall",
                         "preview", "project", "remediation", "repositoryedit", "scm", "secret", "usage", "workflow"},
    "maintenance":      {"console", "conversation", "deployment", "project", "scm", "tasks", "workflow"},
    "workcommands":     {"adoption", "conformance", "integration", "preview", "project", "projectlifecycle",
                         "remediation", "repositoryedit", "scm", "verification", "workflow"},
    "inference":        {"console", "conversation", "integration", "modelcall", "secret", "usage", "workflow"},
    "changeadds":       {"adoption", "secret", "workflow"},
    "evals":            {"adoption", "complaint", "conformance", "project", "projectlifecycle", "scm", "usage",
                         "verification", "workflow", "wow"},
}

def top(pkg):
    """internal/forge/deployment/docker -> deployment; internal/forge/wow/internal/wowsql -> wow;
    internal/console/pi -> console; internal/forge -> forge (the App)"""
    rest = pkg[len(MOD):]
    if rest == "forge":
        return "forge"
    if rest.startswith("forge/"):
        rest = rest[len("forge/"):]
    return rest.split("/")[0]

def pkg_dir(name):
    """Where a named package lives: libraries at internal/<x>, the rest under internal/forge/."""
    return os.path.join("internal", name) if name in LIBRARIES else os.path.join("internal", "forge", name)

def go_imports(pkg):
    # Architecture arrows are direct dependencies. Work -> Adoption -> Ways
    # does not mean Work imports Ways; that distinction is the boundary.
    out = subprocess.run(["go", "list", "-f", '{{join .Imports "\\n"}}',
                          "./" + pkg_dir(pkg) + "/..."], cwd=ROOT,
                         capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError("cannot inspect " + pkg + ": " + out.stderr.strip())
    return {top(l) for l in out.stdout.split() if l.startswith(MOD)}


def inventory():
    """Every package under internal/ and its direct imports, tests included: a library
    whose tests need forge's schema or fixtures is not a library."""
    out = subprocess.run(["go", "list", "-f",
                          '{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}',
                          "./internal/..."],
                         cwd=ROOT, capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError("cannot inspect internal packages: " + out.stderr.strip())
    return {line.split()[0]: line.split()[1:] for line in out.stdout.splitlines()}

def forge_importers():
    # This rule applies even to a helper package that is not a named subsystem.
    return sorted(p for p, deps in inventory().items() if APP in deps)

def layering():
    v = []
    for p, deps in sorted(inventory().items()):
        if p[len(MOD):].split("/")[0] == "forge":
            continue
        for d in sorted(deps):
            if d == APP or d.startswith(DOMAIN):
                v.append(f"{p} imports {d} (a library knows nothing of forge)")
    return v

def doors():
    v = [f"{p} imports internal/forge" for p in forge_importers()]
    present = [p for p in sorted(set(SUBSYSTEMS + FACADES) | KERNELS) if os.path.isdir(os.path.join(ROOT, pkg_dir(p)))]
    for p in present:
        deps = go_imports(p)
        for d in sorted(deps):
            if d == p:
                continue
            if p in KERNELS and (d in set(SUBSYSTEMS + FACADES) - KERNELS or d not in LEAVES | KERNELS | LIBRARIES | {"forge"}):
                v.append(f"{p} -> {d} (a kernel may import only leaves and kernels)")
                continue
            if d in LEAVES or d in KERNELS:
                continue
            if d == "forge":
                continue  # reported once by the whole-internal-package rule
            if p in FACADES:
                # internal/console (a library) and internal/forge/console (a
                # subsystem) share a top name; the allowance lists it either way.
                if d not in ALLOWED.get(p, set()) and not (d in LIBRARIES and d not in SUBSYSTEMS):
                    v.append(f"{p} -> {d} (not in the facade's import allowance)")
                continue
            if d in SUBSYSTEMS and d not in ALLOWED.get(p, set()):
                v.append(f"{p} -> {d} (no arrow in ALLOWED)")
    return v

def transport():
    # Each transport is independent. Domains and sibling transports cannot
    # import it, including through test-only helpers.
    out = []
    imports = inventory()
    for name in ("httpapi", "mcpapi"):
        boundary = DOMAIN + name
        inside = lambda p: p == boundary or p.startswith(boundary + "/")
        out.extend(f"{p} imports {d} ({name} transport may be imported only by forge or itself)"
                   for p, deps in sorted(imports.items())
                   if p != APP and not inside(p)
                   for d in sorted(set(deps)) if inside(d))
    return out


# Who may know a harness. The environment/adapter/orchestration split
# puts rendering for a particular
# harness in the adapter and keeps every domain owner harness-neutral. The
# adapter itself may import nothing of the domain (agentpolicy/harnesswork guard
# that side); this guards the other side. Only the composition root and the two
# orchestration owners that actually launch a harness may import one. doors()
# cannot see this: agentharness is not a subsystem, so an import of it is
# invisible there, and facades are exempt from arrow checks altogether. On
# 17 September inference imported agentharness/pi to render Pi's models.json
# and no gate noticed.
HARNESS_IMPORTERS = {"forge", "agentharness", "workflowexec", "conversation"}


def harness_imports():
    harness = DOMAIN + "agentharness"
    inside = lambda p: p == harness or p.startswith(harness + "/")
    return [f"{p} imports {d} (only the composition root and orchestration owners may know a harness)"
            for p, deps in sorted(inventory().items())
            if top(p) not in HARNESS_IMPORTERS
            for d in sorted(set(deps)) if inside(d)]


def agent_boundaries():
    paths = []
    for package in ("agentenv", "agentharness"):
        base = os.path.join(ROOT, "internal", "forge", package)
        for directory, _, files in os.walk(base):
            paths.extend(os.path.join(directory, f) for f in sorted(files) if f.endswith(".go"))
    result = subprocess.run(["go", "run", "scripts/shape-agents.go", "--", *paths],
                            cwd=ROOT, text=True, capture_output=True, check=True)
    return json.loads(result.stdout)


def methods():
    files = [os.path.relpath(f, ROOT) for f in forge_files()]
    out = subprocess.run(["go", "run", "scripts/shape-methods.go", "--", *files],
                         cwd=ROOT, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def fields():
    """Every exported field on a subsystem's Service struct: a door past its operations."""
    v = []
    for p in SUBSYSTEMS:
        d = os.path.join(ROOT, pkg_dir(p))
        if not os.path.isdir(d):
            continue
        for f in sorted(os.listdir(d)):
            if not f.endswith(".go") or f.endswith("_test.go"):
                continue
            m = re.search(r"^type Service struct \{\n(.*?)^\}", open(os.path.join(d, f)).read(), re.S | re.M)
            if not m:
                continue
            for line in m.group(1).splitlines():
                text = line.strip()
                if not text or text.startswith("//"):
                    continue
                name = text.split()[0].lstrip("*")
                if name[0].isupper():
                    v.append(f"{p}: Service.{name}")
    return v

def forge_files():
    d = os.path.join(ROOT, "internal", "forge")
    return [os.path.join(d, f) for f in sorted(os.listdir(d)) if f.endswith(".go") and not f.endswith("_test.go")]

def constructors():
    pat = re.compile(r"\b(" + "|".join(SUBSYSTEMS) + r")\.New\w*\(")
    v = []
    for f in forge_files():
        if os.path.basename(f) == "app.go":
            with open(f) as source_file:
                source = source_file.read()
            app = re.search(r"type App struct \{(.*?)^\}", source, re.S | re.M)
            if app:
                for field in re.findall(r"(\w+)\s+sync\.Once\b", app.group(1)):
                    if field not in {"consoleCLIOnce", "consoleStopOnce"}:
                        v.append(f"internal/forge/app.go: App.{field} lazily constructs state")
            continue
        for i, line in enumerate(open(f), 1):
            m = pat.search(line)
            if m and not line.lstrip().startswith("//"):
                v.append(f"{os.path.relpath(f, ROOT)}:{i}: {m.group(0)}")
    return v

def aliases():
    # Parse declarations: regexes confuse assignments with aliases and miss
    # grouped type declarations. Keep the helper independent of Go type checks.
    files = [os.path.relpath(f, ROOT) for f in forge_files()]
    out = subprocess.run(["go", "run", "scripts/shape-aliases.go",
                          ",".join(SUBSYSTEMS + FACADES), *files], cwd=ROOT,
                         capture_output=True, text=True, check=True)
    return json.loads(out.stdout)

def callbacks():
    files = [os.path.relpath(f, ROOT) for f in forge_files()]
    out = subprocess.run(["go", "run", "scripts/shape-callbacks.go", "--", *files],
                         cwd=ROOT, capture_output=True, text=True, check=True)
    return json.loads(out.stdout)

# Commands promised to stand alone: what they may not link. forge-worker
# links internal/console since it runs chats' runtimes: that
# package depends on no forge domain or database, and the go list check
# below still refuses anything that would bring one in. It links
# internal/deploy since it boots previews with the forge's own boot code
# (design/plans/isolated-execution-previews-on-workers-2026-09-30.md): the
# library, not the deployment subsystem, which stays refused.
STANDALONE = {"forge-deploy": {"forge", "deployment", "preview", "dataprotection", "database", "workflow", "project", "conformance", "console", "wow", "adoption", "access"},
              "forge-console": {"forge", "workflow", "project", "conformance", "deployment", "preview", "dataprotection", "deploy", "wow", "adoption", "access", "integration", "model"},
              "forge-worker": {"forge", "database", "workflow", "project", "deployment", "preview", "dataprotection", "conformance", "wow", "adoption", "access",
                               "integration", "model", "secret", "scm", "tasks", "identity", "usage", "dependency", "adrs", "complaint"},
              "forge-check": {"forge", "conformance", "workflow", "project", "deployment", "preview", "dataprotection", "database", "console", "wow", "adoption", "access", "integration", "model", "secret", "scm", "usage", "dependency", "adrs", "complaint"}}

def standalone():
    v = []
    for cmd, banned in STANDALONE.items():
        if not os.path.isdir(os.path.join(ROOT, "cmd", cmd)):
            continue
        out = subprocess.run(["go", "list", "-deps", "./cmd/" + cmd], cwd=ROOT, capture_output=True, text=True)
        if out.returncode != 0:
            v.append(f"cmd/{cmd} does not build: {out.stderr.strip().splitlines()[-1] if out.stderr.strip() else ''}"); continue
        for d in sorted({top(l) for l in out.stdout.split() if l.startswith(MOD)}):
            if d in banned:
                v.append(f"cmd/{cmd} links internal/{d}")
    return v

# Statements a package writes by hand instead of naming in its query file
# (design/architecture/record-store-style.md). Three places are legitimately
# raw and are not counted: dbschema writes the migrations, database is the
# driver layer, dbtest creates and drops the databases a test runs in.
HANDWRITTEN_EXEMPT = {"internal/forge/dbschema", "internal/database", "internal/dbtest"}
SQL_STATEMENT = re.compile(r"\b(SELECT\s|INSERT\s+INTO\s|UPDATE\s+[a-z_]+\s+SET|DELETE\s+FROM\s)", re.I)

def handwritten():
    # Reuse the Go parser from schema ownership: comments and Go select blocks
    # are not SQL. Count statement-shaped literals, including nested statements;
    # a SELECT report needs FROM or an expression (function, literal, parameter).
    result = subprocess.run(["go", "run", os.path.join(ROOT, "scripts/schema-sql-literals.go"), ROOT],
                            cwd=ROOT, text=True, capture_output=True, check=True)
    return handwritten_items(json.loads(result.stdout))


def handwritten_items(items):
    v = []
    select_sql = re.compile(r"\bSELECT\s+(?:$|[\s\S]*?\bFROM\b|(?:DISTINCT\s+)?(?:[\w.]+\s*\(|[0-9('$]|TRUE\b|FALSE\b|NULL\b))", re.I)
    for item in items:
        if item.get("dynamic") or os.path.dirname(item["file"]) in HANDWRITTEN_EXEMPT:
            continue
        body = item["text"]
        for statement in SQL_STATEMENT.finditer(body):
            if statement[0].lower().startswith("select") and not select_sql.match(body, statement.start()):
                continue
            v.append(f'{item["file"]}:{item["line"]}: handwritten SQL')
    return v


# Test SQL. The programme that moved storage guarantees to their owners
# left two kinds of SQL in
# tests. Ordinary SETUP — rows fabricated to reach a state a domain operation
# could have produced — is debt: it bypasses the operation whose behaviour the
# test depends on. The rest is deliberate: state no valid operation can produce
# (malformed JSON, invalid stored vocabulary), or state changed mid-flight to
# prove a fence catches it. That residue stays, but it must SAY SO at the site,
# so the retained-SQL audit is enforced rather than remembered.
#
# A write leaves the count two ways: drive the state through the owner's
# operation, or declare the reason with a `sql-guarantee:` comment on the
# statement or the three lines above it. Reads are counted separately and are
# NOT a zero target: an owner's storage test asserting through SQL is the
# independent check the programme deliberately preserved.
TEST_SQL_EXEMPT = HANDWRITTEN_EXEMPT
TEST_SQL_MARKER = re.compile(r"sql-guarantee:\s*\S", re.I)
TEST_WRITE = re.compile(r"\b(INSERT\s+INTO\s|UPDATE\s+[a-z_]+\s+SET|DELETE\s+FROM\s)", re.I)
# A fault injector is handed the TEXT of a statement to fail. That string names
# a statement; it does not run one, so it is not test SQL.
FAULT_CALLS = {"failNextStatement", "FailNextStatement", "WaitForLock", "failStatement"}
# The same distinction structurally, for selectors built outside a call: a real
# statement is complete ("INSERT INTO t(..) VALUES (..)"), a selector is a
# prefix ("INSERT INTO jobs", "UPDATE changes SET state=").
COMPLETE = {
    "insert": re.compile(r"\bINSERT\s+INTO\s+[\w.\"]+[\s\S]*?\b(VALUES|SELECT|DEFAULT\s+VALUES)\b", re.I),
    "update": re.compile(r"\bUPDATE\s+[a-z_]+\s+SET\s+[\s\S]*?=\s*\S", re.I),
    "delete": re.compile(r"\bDELETE\s+FROM\s+[\w.\"]+\s*\S", re.I),
}


def _executed(body, statement):
    """True when this really is a statement the test runs, not a name for one."""
    head = statement[0].strip().split()[0].lower()
    rule = COMPLETE["insert" if head == "insert" else "update" if head == "update" else "delete"]
    return bool(rule.match(body, statement.start()))


def _source_lines(path, _cache={}):
    if path not in _cache:
        _cache[path] = open(os.path.join(ROOT, path), encoding="utf-8", errors="ignore").read().split("\n")
    return _cache[path]


def _declared(item, source=_source_lines):
    lines = source(item["file"])
    # Same line, or the line directly above it. A wider window lets one marker
    # silence the next statement, which would make the gate meaningless.
    i = item["line"] - 1
    return any(TEST_SQL_MARKER.search(l) for l in lines[max(0, i - 1):i + 1])


def test_sql():
    result = subprocess.run(["go", "run", os.path.join(ROOT, "scripts/schema-sql-literals.go"), "-tests", ROOT],
                            cwd=ROOT, text=True, capture_output=True, check=True)
    return test_sql_items(json.loads(result.stdout))


def test_sql_items(items, source=_source_lines):
    select_sql = re.compile(r"\bSELECT\s+(?:$|[\s\S]*?\bFROM\b|(?:DISTINCT\s+)?(?:[\w.]+\s*\(|[0-9('$]|TRUE\b|FALSE\b|NULL\b))", re.I)
    writes, reads = [], []
    for item in items:
        if item.get("dynamic") or os.path.dirname(item["file"]) in TEST_SQL_EXEMPT:
            continue
        body = item["text"]
        if item.get("call") in FAULT_CALLS:
            continue
        for statement in TEST_WRITE.finditer(body):
            if not _executed(body, statement):
                continue
            if not _declared(item, source):
                writes.append(f'{item["file"]}:{item["line"]}: undeclared test write ({statement[0].split()[0].upper()})')
        for statement in re.finditer(r"\bSELECT\s", body, re.I):
            if select_sql.match(body, statement.start()):
                reads.append(f'{item["file"]}:{item["line"]}: test read')
    return writes, reads


# Test placement. A test whose subject has left the package is invisible to every
# other gate: the production extraction moves a file, its test stays behind, and
# nothing counts it. Root composition tests are legitimate and common — they
# assemble the App and drive several owners — so this counts only the narrow case
# that cannot be one: no production counterpart in root, no App or harness
# construction, and at most one domain package imported. That is a unit test
# whose owner is somewhere else.
TEST_APP_BUILD = re.compile(r"\bnewTestApp\b|\bnewApp\b|\bNew\(|testApp\(", re.I)


def app_builders(_cache=[]):
    """Root test helpers that hand back an *App. Naming them by hand misses
    conformanceTestApp and completionReleaseScenario; the return type does not."""
    if not _cache:
        names = set()
        root = os.path.join(ROOT, "internal", "forge")
        if os.path.isdir(root):
            for name in os.listdir(root):
                if not name.endswith("_test.go"):
                    continue
                with open(os.path.join(root, name), encoding="utf-8", errors="ignore") as handle:
                    body = handle.read()
                for decl in re.finditer(r"^func ([a-zA-Z_]\w*)\(([^\n]*?)\)\s*(\(?[^{\n]*)", body, re.M):
                    if re.search(r"\*App\b", decl.group(3)):
                        names.add(decl.group(1))
        _cache.append(names)
    return _cache[0]
TEST_DOMAIN_IMPORT = re.compile(r'flokkit\.io/forge/internal/forge/(\w+)"')


def testhomes():
    v = []
    root = os.path.join(ROOT, "internal", "forge")
    for name in sorted(os.listdir(root)):
        if not name.endswith("_test.go"):
            continue
        path = os.path.join(root, name)
        if os.path.exists(path[: -len("_test.go")] + ".go"):
            continue
        with open(path, encoding="utf-8", errors="ignore") as handle:
            source = handle.read()
        # A file declaring no tests is a fixture. Fixtures belong beside the
        # tests that consume them, so they have no home of their own to be in.
        if not re.search(r"^func Test\w+\(", source, re.M):
            continue
        if TEST_APP_BUILD.search(source):
            continue
        if app_builders() & set(re.findall(r"(?<![.\w])([A-Za-z_]\w*)\s*\(", source)):
            continue
        if len(set(TEST_DOMAIN_IMPORT.findall(source))) > 1:
            continue
        # A matching filename is not the test of home. What matters is whether
        # the subject still lives here: a test that exercises identifiers this
        # package declares is at home whatever the file is called, and a test
        # that declares its own fixtures serves the root tests that use them.
        # Bare references only: identity.Service is the identity package's, not
        # this one's, however many root types happen to be called Service.
        if root_symbols() & set(re.findall(r"(?<![.\w])([A-Za-z_]\w*)\s*[({]", source)):
            continue
        v.append(f"internal/forge/{name}: subject is not in this package")
    return v


def root_symbols(_cache=[]):
    """Every top-level name internal/forge's production files declare."""
    if not _cache:
        names = set()
        for path in forge_files():
            with open(path, encoding="utf-8", errors="ignore") as handle:
                body = handle.read()
            names |= set(re.findall(r"^func (?:\([^)]*\)\s*)?([A-Za-z_]\w*)", body, re.M))
            names |= set(re.findall(r"^type ([A-Za-z_]\w*)", body, re.M))
        _cache.append(names)
    return _cache[0]


# The root's size. Every other rule counts a particular bypass (an App method,
# a retained callback, a constructor outside app.go); none counts what simply
# lands in the root, so a new file of domain behaviour or another round of
# owner tests that build an App passed all of them. These two are plain counts
# at today's value: a new root file needs one retired, or a baseline raised in
# the same commit where review can see it.
ROOT_HINT = ("internal/forge is the composition root (design/architecture/layout.md, "
             "\"The three places code lives\"). Put domain behaviour and its tests in the owner "
             "package, transport code in httpapi/mcpapi; if this file really is assembly or a "
             "cross-owner guarantee, merge it into an existing root file or raise the baseline "
             "in scripts/shape-baseline.json in this commit and say why.")
HINTS = {"rootfiles": ROOT_HINT, "roottests": ROOT_HINT,
         "droppederrors": "a dropped error from a decode (its zero value stands in for what was stored: an empty set, no pins, "
                          "no tests) or from a revocation, provider call or record write (a live credential or a fact silently lost). "
                          "Handle it; where dropping is safe, say why at the site and raise the baseline in this commit. -v lists the sites.",
         "testexecfixtures": "a new script a test runs (execfile.Write): a test that needs only an answer from docker or git "
                             "uses the adapter's in-process fake (internal/deploy/docker/dockerfake, an execution.DockerCLI func); "
                             "keep a script only where the process boundary (argv, streams, environment, exit, cancellation) is the guarantee. "
                             "-v lists the sites."}


def root_files(root=None, tests=False):
    d = os.path.join(root or ROOT, "internal", "forge")
    return [f"internal/forge/{name}" for name in sorted(os.listdir(d))
            if name.endswith(".go") and name.endswith("_test.go") == tests
            and os.path.isfile(os.path.join(d, name))]


def request_boundaries():
    paths = list(forge_files())
    for package in ("httpapi", "mcpapi"):
        directory = os.path.join(ROOT, "internal", "forge", package)
        paths.extend(os.path.join(directory, name) for name in os.listdir(directory)
                     if name.endswith(".go") and not name.endswith("_test.go"))
    result = subprocess.run(["go", "run", "scripts/shape-request-boundaries.go", "--", *paths],
                            cwd=ROOT, capture_output=True, text=True, check=True)
    return json.loads(result.stdout)


# Ratchets on transport and owner debt: these counts can only fall. Each
# counts production Go only, and a comment is not code.

def go_sources(*dirs, root=None, recursive=False):
    """Production .go files under each directory, relative to the repository."""
    base = root or ROOT
    out = []
    for d in dirs:
        top_dir = os.path.join(base, d)
        if not os.path.isdir(top_dir):
            continue
        walk = os.walk(top_dir) if recursive else [(top_dir, [], os.listdir(top_dir))]
        for directory, _, names in walk:
            out.extend(os.path.relpath(os.path.join(directory, n), base) for n in sorted(names)
                       if n.endswith(".go") and not n.endswith("_test.go")
                       and os.path.isfile(os.path.join(directory, n)))
    return sorted(out)


def code_lines(path, root=None):
    """(line number, text) for each line that is not wholly a comment."""
    with open(os.path.join(root or ROOT, path), encoding="utf-8", errors="ignore") as handle:
        for i, line in enumerate(handle, 1):
            if not line.lstrip().startswith("//"):
                yield i, line


# A decode whose error is dropped leaves its zero value standing in for what
# was stored or sent: an empty set, no pins, no tests. Where that zero value is
# safe the site says why and stays counted; a new one handles its error.
# The same holds for a dropped answer from what revokes a credential, calls a
# provider or writes a record: an owner's SQL write, a Do, or a method named
# record/Record/Note/End/Revoke/persist. Best-effort ones say so and stay counted.
DROPPED_DECODE = re.compile(r"\b_\s*(?:,\s*_\s*)?=\s*(?:json|xml)\.(?:Unmarshal\(|NewDecoder\([^)]*\)\.Decode\()")
DROPPED_ASSIGN = re.compile(r"^\s*_\s*(?:,\s*_\s*)*=\s*(\S.*)$")
DROPPED_EFFECT = re.compile(r"^(?:\w+sql\.New\(|gitlabJSON\(|(?:[\w]+\.)*(?:Do|DoContext|[Rr]ecord\w*|Note\w*|End\w*|Revoke\w*|[Pp]ersist)\()")


def dropped_decodes(root=None):
    out = []
    for f in go_sources("internal", "cmd", root=root, recursive=True):
        if f.endswith(".sql.go"):
            continue
        for i, line in code_lines(f, root):
            m = DROPPED_ASSIGN.match(line)
            if DROPPED_DECODE.search(line) or (m and DROPPED_EFFECT.match(m.group(1))):
                out.append(f"{f}:{i}: {line.strip()}")
    return out


TRANSPORTS = (os.path.join("internal", "forge", "httpapi"), os.path.join("internal", "forge", "mcpapi"))
APP_DIR = os.path.join("internal", "forge")
AUDIT_IN_TRANSPORT = re.compile(r"\baudit\.(BestEffort|Recent)\(")


def transport_audit(root=None):
    """A transport that writes a receipt best-effort, or reads the log itself,
    has taken an owner's job: the owner records its act in its own transaction."""
    return [f"{f}:{i}: {m.group(0)}" for f in go_sources(APP_DIR, *TRANSPORTS, root=root)
            for i, line in code_lines(f, root) for m in AUDIT_IN_TRANSPORT.finditer(line)]


RAW_DB = re.compile(r"\*database\.DB\b|\bdatabase\.Queryer\b")
DEPENDENCY_FIELD = re.compile(r"^\s+[A-Z]\w*(?:,\s*[A-Z]\w*)*\s+(?:\*database\.DB|database\.Queryer)\b")


def transport_db(root=None):
    """A transport constructed with the raw database: a constructor parameter, or
    an exported field of a dependencies struct. Transports call owners."""
    v = []
    for f in go_sources(*TRANSPORTS, root=root):
        with open(os.path.join(root or ROOT, f), encoding="utf-8", errors="ignore") as handle:
            source = handle.read()
        for m in re.finditer(r"^func (New\w*)\(", source, re.M):
            depth, j = 1, m.end()
            while j < len(source) and depth:
                depth += {"(": 1, ")": -1}.get(source[j], 0)
                j += 1
            for _ in RAW_DB.finditer(source[m.end():j]):
                v.append(f"{f}:{source.count(chr(10), 0, m.start()) + 1}: {m.group(1)} takes the raw database")
        for i, line in code_lines(f, root):
            if DEPENDENCY_FIELD.match(line):
                v.append(f"{f}:{i}: dependency field holds the raw database")
    return v


# Where a project's repository lives on disk is the project's to say. Every
# other package spelling the path is a copy that drifts.
HUB_PATH = re.compile(r'"workspaces",\s*"project-"')
HUB_PATH_OWNER = os.path.join("internal", "forge", "project")


def hub_paths(root=None):
    return [f"{f}:{i}: spells the project repository path" for f in go_sources("internal", "cmd", root=root, recursive=True)
            if not f.startswith(HUB_PATH_OWNER + os.sep)
            for i, line in code_lines(f, root) if HUB_PATH.search(line)]


# Generic deployment code that branches on a target's kind instead of asking
# its driver (design/subsystems/deploy-targets.md: a provider is an entry and a driver).
# Data protection and Preview are deployment code split out of that
# package; their branches on a target's kind count the same.
KIND_BRANCH = re.compile(r"\.Kind\s*[!=]=\s*(?:deployment\.)?Kind\w+")
KIND_BRANCH_DIRS = ["deployment", "dataprotection", "preview"]


def kind_branches(root=None):
    return [f"{f}:{i}: {m.group(0)}" for d in KIND_BRANCH_DIRS for f in go_sources(os.path.join("internal", "forge", d), root=root, recursive=True)
            for i, line in code_lines(f, root) for m in KIND_BRANCH.finditer(line)]


# Comments explain why the code is as it is. Who reviewed it, in which round,
# under which finding id or on which date is history; it belongs in the commit
# and in design/reviews, and a production comment citing a plan treats a
# proposal as the current manual (CLAUDE.md, "Writing and maintaining guidance").
HISTORY = re.compile(r"Codex|\b[Rr]ound [0-9]+\b|review of step|\(run [0-9]|\b[A-Z]{1,4}[0-9]?-[0-9]{2}\b"
                     r"|\b20[0-9]{2}-[01][0-9]-[0-3][0-9]\b|design/(?:plans|reviews)/"
                     r"|\b(?:January|February|March|April|May|June|July|August|September|October|November|December) 20[0-9]{2}\b")
COMMENT = re.compile(r"(?:^|\s)//(.*)$")


def history_comments(root=None):
    v = []
    for f in go_sources("internal", "cmd", root=root, recursive=True):
        with open(os.path.join(root or ROOT, f), encoding="utf-8", errors="ignore") as handle:
            for i, line in enumerate(handle, 1):
                m = COMMENT.search(line)
                if m and HISTORY.search(m.group(1)):
                    v.append(f"{f}:{i}: {m.group(1).strip()[:80]}")
    return v


# A test that writes a file with os.WriteFile and then runs it can fail on
# Linux with "text file busy" (ETXTBSY, Go issue 22315): a goroutine forking
# while the file is open hands the write descriptor to a child until that
# child's exec. internal/execfile.Write writes it with no fork in between.
# testexecwrites refuses any other write with an executable mode in a test or
# a test helper package; testexecfixtures counts the execfile.Write calls
# there, so the tests that start a fake docker or git may only fall: a test
# that needs only an answer from docker or git uses the adapter's in-process
# fake, and a script stays where the process boundary is the guarantee.
TEST_FILE = re.compile(r"(_test\.go$|(test|fake)/[^/]+\.go$)")
FILE_WRITE = re.compile(r"\bos\.(WriteFile|OpenFile)\(")
EXEC_MODE = re.compile(r"^0o?[0-7]?[1357][0-7]{2}$")
EXEC_FIXTURE = re.compile(r"\bexecfile\.Write\(")


def test_go_sources(root=None):
    base = root or ROOT
    out = []
    for d in ("internal", "cmd"):
        for directory, _, names in os.walk(os.path.join(base, d)):
            for n in names:
                rel = os.path.relpath(os.path.join(directory, n), base)
                if n.endswith(".go") and TEST_FILE.search(rel.replace(os.sep, "/")):
                    out.append(rel)
    return sorted(out)


def _call_args(text, start):
    """The text between the parenthesis at start and its match, with string
    literals kept and comments outside them dropped."""
    depth, i, quote, out = 1, start + 1, None, []
    while i < len(text) and depth:
        c = text[i]
        if quote:
            if c == "\\" and quote != "`":
                out.append(text[i:i + 2])
                i += 2
                continue
            if c == quote:
                quote = None
        elif text.startswith("//", i):
            i = text.find("\n", i)
            i = len(text) if i < 0 else i
            continue
        elif text.startswith("/*", i):
            i = text.find("*/", i)
            i = len(text) if i < 0 else i + 2
            continue
        elif c in "\"`'":
            quote = c
        elif c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
        out.append(c)
        i += 1
    return "".join(out)[:-1]


def _without_comments(path, root):
    return "".join(line if not line.lstrip().startswith("//") else "\n"
                   for _, line in _numbered(path, root))


def _numbered(path, root):
    with open(os.path.join(root or ROOT, path), encoding="utf-8", errors="ignore") as handle:
        yield from enumerate(handle, 1)


def test_exec_writes(root=None):
    v = []
    for f in test_go_sources(root):
        text = _without_comments(f, root)
        for m in FILE_WRITE.finditer(text):
            mode = _call_args(text, m.end() - 1).strip().rstrip(",").rsplit(",", 1)[-1].strip()
            if EXEC_MODE.match(mode):
                v.append(f"{f}:{text.count(chr(10), 0, m.start()) + 1}: os.{m.group(1)} with mode {mode}: use execfile.Write")
    return v


def test_exec_fixtures(root=None):
    return [f"{f}:{text.count(chr(10), 0, m.start()) + 1}: execfile.Write"
            for f in test_go_sources(root) for text in [_without_comments(f, root)]
            for m in EXEC_FIXTURE.finditer(text)]


def main():
    tighten = "--tighten" in sys.argv
    verbose = "-v" in sys.argv
    test_writes, test_reads = test_sql()
    agents = agent_boundaries()
    requests = request_boundaries()
    counts = {"layering": layering(), "doors": doors(), "transport": transport(), "methods": methods(), "fields": fields(), "constructors": constructors(), "aliases": aliases(), "standalone": standalone(), "callbacks": callbacks(), "handwritten": handwritten(), "testwrites": test_writes, "testreads": test_reads, "agentpolicy": agents["policy"], "harnesswork": agents["settlement"], "testhomes": testhomes(), "harnessimports": harness_imports(), "jsondecode": requests["jsondecode"], "routepolicies": requests["routepolicies"], "rootfiles": root_files(), "roottests": root_files(tests=True), "transportaudit": transport_audit(), "transportdb": transport_db(), "hubpaths": hub_paths(), "kindbranches": kind_branches(), "historycomments": history_comments(), "testexecwrites": test_exec_writes(), "testexecfixtures": test_exec_fixtures(), "droppederrors": dropped_decodes()}
    path = os.path.join(ROOT, "scripts", "shape-baseline.json")
    baseline = json.load(open(path)) if os.path.exists(path) else {}
    ok = True
    for rule, found in counts.items():
        n, b = len(found), baseline.get(rule)
        mark = "ok" if b is None or n <= b else "WORSE"
        print(f"shape {rule:15} {n:4}  baseline {b if b is not None else '-':>4}  {mark}")
        if b is not None and n > b:
            ok = False
            if rule in HINTS:
                print("   ", HINTS[rule])
        # A count rule's list is every file, not the offending one: -v shows it.
        if verbose or (b is not None and n > b and rule not in HINTS):
            for line in found:
                print("   ", line)
    if tighten or not baseline:
        json.dump({k: len(v) for k, v in counts.items()}, open(path, "w"), indent=2)
        print("baseline written:", os.path.relpath(path, ROOT))
    sys.exit(0 if ok else 1)

if __name__ == "__main__":
    main()
