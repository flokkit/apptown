# Layout: where code goes and what it is called

Apply these rules to code you touch; they are not a mandate for a
repository-wide cosmetic rename.

[domains.md](domains.md) is the map of what owns what and the principles behind
the boundaries. This page answers a narrower question: *I am about to add a
file — where does it go and what is it called?*

Some boundaries have mechanical checks; others require review. A **gate** is an
automated rule with a baseline in `scripts/shape-baseline.json`. `make check`
fails when a measured count grows. Tighten the baseline after deleting bypasses;
do not hide bypasses behind helpers. Additional independent persistence assertions
may increase `testreads`, with their guarantee and reason stated at the site.

Literal shell success suppression has a separate hard rule: tracked `.sh` files
using `|| true` or `|| :` need a nonempty failure contract in
`scripts/shell-suppression-sites.json`, keyed by exact file and physical source
line. Stale, duplicate and ambiguous declarations fail; no count baseline or
`--tighten` allowance applies. This is a conservative text check, including
quoted traps and remote payloads, not a shell parser. Full-line comments are
excluded; split-line operators, other success commands, conditionals and scripts
embedded only in other file types are not covered. A reason explains the actual
failure consequence; it cannot turn unavailable evidence into success.

The checks have specific limits. `fields` counts exported fields on registered
subsystem Service types, not fields on App. `constructors` checks registered
subsystem construction outside App.New and forbidden App `sync.Once` fields; it
does not prove that every object in every package is eagerly built. `callbacks`
counts dependency bindings retaining App, including method values and closures.
`rootfiles` and `roottests` count the production and test files in
`internal/apptown` itself; they catch a new root file, not growth inside an
existing one, and a merge that hides a concern is a bypass.
The gates do not prove marker explanations, test ownership or semantic layering.

Starting a process is owned too. `scripts/exec-check.go` (`make check-exec`)
counts, in production and test files alike, every `exec.Command`,
`exec.CommandContext`, `exec.Cmd` used as a value, `os.StartProcess` and
`syscall` exec per package, and refuses git started by name anywhere but
`gitexec.Command`. It catches process starts written in this module in the
ordinary ways; it is not a defence against deliberate evasion. A command
value built from a type another package exports (a wrapper embedding
`exec.Cmd`, an alias, a named list of commands), containers of commands
passed around, reflection, cgo and raw syscalls are out of scope and are
review's job. `scripts/exec-baseline.json` lists the packages that own
starting processes, each with its reason (`gitexec` for git, `execution` for
any command, `deploy/docker` for the docker CLI, `worker`, `console/pi`);
every other package is held at its count there, which may fall and may not
rise. Start a process through an owner's interface (`gittest` for a test's
git, `dockertest` or `docker.CLI` for a test's docker, `execution.Runner` for
a command). `-tighten` records a fall; a rise is
a hand edit with its reason.

Reading what a container answered is owned by `internal/containerresult`.
Docker can lose a short-lived container's stdout at its end and still
report exit 0, so a value, version or list is written to a file in the
container and copied out with `docker cp` after it exits
(`docker.Scope.Result`, `ExecResult`), and a copy of data out of a
container carries a trailer its receiver checks (`docker.Scope.RunStep`).
`scripts/container-output-check.go` (`make check-container-output`) refuses
production code elsewhere that reads the output of a `docker run` or
`docker exec` as a result: one whose output is used other than in error
text, or streamed out through a `docker.Command`'s `Stdout`. Detached runs,
terminals and a method answering only success are not reads. It has no
baseline. A command built in another function, a process started with
`exec.Command`, `docker logs` and deliberate evasion are out of its reach
and are review's. Pass/fail work (scripts, checks, tests) keeps its exit
code as the verdict, and a failure whose output did not arrive says
"report lost" (`check.ReportLost`).

Time is owned as well. An owner reads the time only through a
`now func() time.Time` it takes from its dependencies, defaulted with
`clock.Or` (`internal/clock`, the one package that names `time.Now`), so a
test can set it; production code never calls `time.Now`, `time.Since` or
`time.Until` itself. A stored time a rule compares is written from that
clock and passed to SQL, so both sides of the comparison are AppTown's.
SQL reads no clock of its own: no `now()` or `CURRENT_TIMESTAMP` in a query,
a column default, a trigger or a view, so every stamp is written by
AppTown (a column is `NOT NULL` without a default, or null where absence means
something). Migrations already applied are history and are exempt;
`make check-sqlnow` (`scripts/sqlnow-check.go`) fails on any read in the
rest; `scripts/sqlnow-baseline.json` records only the number the
migrations are frozen through and a digest of what they say. A context is
given the time left until an owner's deadline, never the
deadline itself, which it would read on the real clock. `clock.System` is
the one named exception: comparing with a time the operating system
stamped, such as a file's modification time. Nothing calls `time.Sleep`,
tests included: production waits on a timer in a `select` with `ctx.Done` (`poll.Wait`, or `poll.Until`
for a condition), and a test polls with a
deadline (`poll.Until`) or lets time pass under `testing/synctest`. A test
that something does not happen witnesses the owner's decision instead of
waiting a while: its done signal, the refusal or record it makes when it
does not act, its fence, or an effect made idempotent and asked for
twice. A bounded wait for nothing (`polltest.Holds`) is a sleep in
disguise, allowed only for asynchrony nothing owns, at sites allow-listed
with their reasons in `.golangci.yml`. Production work runs on its
caller's context, so a cancelled request or a stopping AppTown stops it;
only an entry point (a process's main, an owner's lifetime) makes a fresh
one with `context.Background`, allow-listed by site in `.golangci.yml`.
Work that must finish though its caller stops takes
`context.WithoutCancel(ctx)`, which keeps the caller's values. These rules are forbidigo's
(`.golangci.yml`, `make check-lint`). The clock and context rules are hard:
any violation fails and `scripts/lint-baseline.json` may hold no count for
them; what predates the others is held to per-package counts there.

Informational audit notes (`audit.At.BestEffort`) and repeated settlement
writes (`workflow.Settlement.Retry`) also require exact production sites
in `.golangci.yml`, with a reason naming the retained evidence or the
repeat/failure contract. These are hard rules without count baselines.
The checker rejects missing or ambiguous sites and directory exemptions;
tests and test helpers may exercise the APIs directly. The declarations
require human review: they do not prove a dynamic event kind is informational,
an arbitrary callback is safe to repeat, or all discarded errors and shell
suppression are covered. Other APIs named Retry are not this rule.

Ignored error returns in the direct production files of `internal/database`
and `internal/gitexec` are checked by errcheck, including bare calls and blank
assignments. Only the infallible strings.Builder WriteString and WriteByte
methods are excluded. An intentional discarded error needs a same-line
`// unchecked-error: <failure contract>` comment: the driver matches it to
exactly one analyzer diagnostic. Empty, stale, ambiguous or function-wide
reasons and broad nolint directives fail. The scan honors the analyzed build's
file selection. Reasons require human review; moving a declared call is not
semantic proof that its reason remains true. This first scope excludes tests,
helper subpackages and other production owners; it introduces no count
allowance and does not certify those unactivated areas or generated output.

## The three places code lives

**`internal/<x>` — a library.** Knows nothing of AppTown. No AppTown imports,
no tables, no vocabulary from the product. `access`, `check`, `clock`, `console`,
`database`, `dbtest`, `deploy`, `diskspace`, `dockerbuild`, `dockerlabel`, `durable`, `enum`, `execfile`, `execution`, `fault`, `gitexec`, `hostmem`, `oci`, `poll`, `progress`, `stamp`, `strs`, `trailer`, `wiregen`, `worker`. A library can
be read, tested and understood without knowing what a Change is.
*Gate: `layering`.*

**`internal/apptown/<x>` — a subsystem.** AppTown-specific. Usually owns tables,
but **owning tables is not the entry requirement**: being AppTown-specific is. `httpapi` owns no tables and belongs here.
*Gates: `doors`, `fields`, `transportaudit`, `transportdb`, `hubpaths`, `kindbranches`.*

**`internal/apptown` — the composition root.** Construction, route declarations,
authentication, instance startup/shutdown and protocol wiring are deliberate
responsibilities. Remaining application operations must have a documented owner
or a specific retained reason. Nothing arrives here by default.
A protocol handler here keeps the protocol and asks the owner for the rule:
the Git handler asks `workflow` what a step's token may push.
A new file here raises `rootfiles` or `roottests`: retire one, or raise the
baseline in the same commit and say why the file is assembly or a cross-owner
guarantee.
*Gates: `methods`, `callbacks`, `constructors`, `rootfiles`, `roottests`.*

Two subsystems have narrower rules. A **kernel** (`audit`, `identity`) may be
imported by any subsystem and may import only leaves, libraries and kernels. A
**facade** (`projectlifecycle`, `remediation`, `changeconsole`) composes several subsystems and may import its participants. It owns the
ordering of transactions and external effects, not a promise that an entire
operation fits inside one transaction. Each facade's participants are listed
in `scripts/shape-check.py` (`ALLOWED`); a new import is a visible change
there, not something `doors` waves through.

Four counts keep these shortcuts from coming back. They can only fall:
`transportaudit` (best-effort receipts and audit reads in the root and the
transports, where the owner should record its act), `transportdb` (transport
constructors handed the raw database), `hubpaths` (the project repository path
spelled outside the project owner) and `kindbranches` (deployment code branching
on a target's kind instead of asking its driver). `historycomments` is held at
zero: a production comment says why, not who reviewed it, in which round or on
which date, and cites the current guide rather than a plan.

## When something becomes a library

Promote `internal/apptown/<x>` to `internal/<x>` when **all** hold:

1. It names nothing from the product. No Change, Run, Project, Adoption — in
   its types, its errors, or its words.
2. It owns no AppTown tables.
3. Its tests need no AppTown schema or fixtures.
4. Something outside AppTown could plausibly use it, even if nothing does.

`check` is the worked example: the judge was App code, then a subsystem, then a
library, because a rule evaluated against evidence never needed to know whose
project it was. `deploy` is another: run a described app on a target, no
records. The shape check's `standalone` rule lists what each library may not
link, directly or through its imports.

Do not promote for size. A large subsystem is a large subsystem. Promote when
the product vocabulary genuinely drops out — and if removing it takes a
find-and-replace, it had not dropped out.

## Inside a subsystem

    internal/apptown/<x>/
        <x>.go                  the Service, its construction, its doors
        <concept>.go            one concept per file
        <concept>_commands.go   named command inputs and the operations taking them
        internal/<x>sql/        generated queries — never imported from outside <x>
        *_test.go              beside the code they test

**Named command inputs.** Group related or ambiguous arguments into a named
struct: `Bootstrap(in NewAccount)`, `SaveUser(in AccountChange)`. Keep simple
identifier lookups idiomatic. Avoid a universal input with flags for unrelated
operations. Validation errors get a name when callers need to distinguish them.

**SQL is generated.** Named queries in `queries.sql` (or an owner's per-table
query files), bound through the
generated package. Hand-written SQL is not a style choice that lost; it is
zero in production, and `handwritten` holds that boundary. `testwrites`
separately counts undeclared setup bypasses; independent SQL assertions remain
valuable in persistence tests.

**Exported fields are a door past the operations.** A subsystem's Service
exports behaviour, not parts. `fields` ratchets the registered Service types;
plain dependency inputs and value records have different responsibilities.

## Inside transport

`internal/apptown/httpapi` turns HTTP requests into domain operations. A handler
group holds only the collaborators its handlers use — a handful should be explainable by one operation. A broad union of
unrelated dependencies is a reason to revisit the grouping, not a fixed quota. It never holds the App, a runtime holder, or a callback
bundle that gives equivalent reach. *Gates: `transport`, `doors`.*

**The standard is most logic outside handlers, not none.**
Logic in a handler is reasonable when all three hold:

1. It is needed in one place only.
2. It is primarily coordination and response shaping.
3. It has no natural better home yet.

Fail any one and it needs an owner. Two transports needing the same operation is
the clearest proof: HTTP and MCP both call `lifecycle.Create`,
`lifecycle.InitializeRepository` and `lifecycle.ApplyPack`, because an operation
only one transport can reach was never an operation. "It only has one caller
today" is condition 1; "it renders a contract into YAML" fails condition 2
whatever its caller count.

`httpapi/compose.go` and the workflow timing/summary projections in
`httpapi/timing.go` are handler logic kept on purpose. Review them when those
features change; a projection does not automatically need another owner.

Route declarations, authentication and guards stay in `internal/apptown`.

## Filenames

One suffix vocabulary, and a file with no suffix is the normal case:

| Suffix | Means |
| --- | --- |
| *(none)* | one concept — `members.go`, `runs.go` |
| `_commands.go` | named command inputs and their operations |
| `_errors.go` | error types, classified with `internal/fault`; transports map kinds in one place |
| `_wiring.go` | assembles dependencies, never implements behaviour |
| `_test.go` | tests, beside what they test |

Prefer these meanings, but keep useful Go platform/build suffixes and explicit
integration, driver and persistence test groupings. Name files for the operation
or guarantee a developer will search for. Do not rename untouched files solely
to hit a suffix count.

## Fakes of external systems

A fake of something outside AppTown — Docker, a cloud API, a coding harness, a
secret store — lives in **its own package beside the code whose contract it
imitates**, named `<system>fake`, and is imported by everyone who needs it.

`internal/deploy/digitalocean/dofake` is the worked example: a small server for
`/v2/account`, `/v2/registry`, `/v2/apps` and `/v2/databases`, used by both the
driver's own tests and the assembled AppTown deployment tests. Its package comment
states the boundary — *"It is not a deployment driver."*

Its own package keeps the exported testing contract separate from the production
driver. Other Go packages can import it without a shared application fixture. Beside the driver, because
the fake exists to satisfy the same contract the driver speaks; when the API
changes, both change together.

A fake of something AppTown owns stays private to its package. There is no
shared fixture package for AppTown's own types, and specifically none that
constructs the App — that reproduces the coupling the ownership work removed.

Docker's reusable protocol fake is `internal/deploy/docker/dockerfake`.
A fake shared by several packages belongs beside the driver; owner-private
collaborator fakes remain in their tests. Do not export constructors for AppTown's
owned records or build a shared App fixture package to make test moves easier.

Every new harness and every new deploy provider wants a fake, and this
convention decides whether that is one package or a growing pile of
near-copies.

## Tests

**Before writing a test, look for it.** State its guarantee in one sentence,
then search the owner package's test names for that sentence's nouns. If
something already asserts it, extend that test or write down why a second one
earns its place.

This works because test names here state guarantees rather than subjects.
Searching `Approval` returns `ApprovalPolicyRequiresCurrentAuthorityAndReceipt`,
`ApprovalRollsBackWhole`, `ApprovalQuorumUnderRace`,
`ApprovalWithdrawalPreservesVotesAndReopensAsANewRound` — fourteen guarantees,
readable without opening a file. A name like `TestCore3` would make the search
useless, which is the real cost of a bad test name.

It also works because a guarantee lives with its owner: the search is one
package, not 225 files. That is the return on test ownership beyond tidiness.

No gate can enforce this. Duplicate coverage is invisible to every mechanical
check: tests with no identical bodies can still assert the same guarantee, and
two hand-written fakes of one cloud API can live in different packages. The
defence is a cheap habit plus names good enough for the habit to work.

Where a vocabulary is enumerable from the code — routes, step types, capability
kinds — prefer a **completeness test** that fails when the vocabulary grows
without coverage: `TestAuthzRouteInventory`, `TestAutoStepTableIsComplete`. That
catches the opposite problem, a missing test rather than a duplicated one, and
it is the one part of this that a machine can hold.

A test lives with the thing it protects. An HTTP test explains what a client
observes; a domain test explains a rule or an operation; a storage test explains
a representation guarantee and lives inside its owner. Ordinary setup goes
through domain operations; where no operation can produce the state — a
malformed row, a fence shoved past its guard — the SQL stays and says why at the
site. The counters detect syntax, not whether that explanation is correct.
Preserve an independent assertion when the storage representation is the
guarantee; converting both setup and observation through the same faulty
operation can make a test circular.

## When in doubt

Ask which package would have to change if this rule changed. That package owns
it. If the answer is "several", it is a facade operation; if the answer is
"none, it's just where it ended up", document the missing owner rather than
pretending the directory decides it. Composition and process lifetime already
have an owner: the root.
