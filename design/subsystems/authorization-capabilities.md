# Authorization decisions

Identity resolves current accounts and grants. The Access library evaluates a
permission against those facts and the caller's credential restrictions. HTTP
and MCP establish authenticated caller context before invoking application code.
An actor string alone is not authority.

Root is a current instance-administrator grant. Credential restrictions apply
before root authority; a restricted token cannot recover permissions from its
owner's broader role. An agent-purpose personal token is restricted like an
OAuth delegation holding every scope, in `access.Decide` and in Identity's
administration check alike (`access.CredentialCovers`), so it never approves,
lands or administers. Merging into the default branch and deploying are acts
a person answers for: the engine performs either only when the run (or the run
that called it) carries a person's approval, or was admitted by someone
holding `change:land`, recorded on the run when it was created. The check is
made at the effect itself, the merge admission and the deploy step's claim, so
it holds whichever door started the run and when it resumes
(`workflow/assent.go`). Starting a run whose definition would reach either
effect before a required approval is also refused early to a caller without
`change:land`. Agents, consoles, step runs and AppTown's own triggers hold no
landing authority and cannot approve. Root may approve its own Change. Non-root approval follows
project eligibility, the independent-approval rule and quorum requirements.

Acting on a chat or on a running step is working on the change, so it needs
`change:invoke-lane` on the project: saying into, pasting into, stopping,
typing into or ending a chat ([console](console.md#looking-and-acting)), and
interrupting a running agent step (`workflow.Service.InterruptStep`). Anyone
who can read the project may look at a chat. The record of each act names the
person who did it, even when a chat runs with its opener's authority. An
operation that spends on a model call before its effect asks for the effect's
authority first: a narrative refresh's source read asks for the narrative's
publication authority, `change:write-narrative`.
That capability reaches a change's "So far" summary (and a title nobody
chose), never the person-written brief (`change:update-brief`). Contributors
hold both; AppTown's own engine holds only the narrative one, so its
automatic refreshes after a run publish, recorded as AppTown's.

Routes declare a capability policy, an owner-checked command, a filtered read,
or an explicit public/protocol contract. MCP tools also declare their policy
contract. These declarations identify where enforcement belongs; an owner/public
exception is not automatic proof its implementation is correct.

Visibility-aware reads select authorized projects before pagination and counts.
Read enumeration records nothing for every project. Consequential
operations preserve their required authorization/audit records. Ordinary record
lookups and transaction participants are not universally self-authorizing APIs.

A read that has a per-project answer is a filtered read, so a person whose
authority covers only their own projects can use it. Usage, the tier map and
departures board, the activity feed, project and run lists, timing, and the
policy library's adoption counts show the caller's readable projects and
nothing of others. Naming a project on one of these reads requires read access
to that project. Usage attributed to no project is instance data: it is
counted only for a caller who holds `project:read` at the instance, and that
caller's report covers the whole instance. A run or chat is bound to its
project and is never such a caller, whatever its person holds. Readable
projects are selected before any limit, using every read a row needs to be
shown (departures need the project and its releases), and a readable change
is not a readable project. The stock role catalogue is
public. Asking who may act on a project needs only read access to it when you
ask about yourself; asking about another person reads the institution's
structure.

Reads about the instance itself keep an instance capability and refuse
everyone else: people, groups and grants, the audit log, deploy targets,
infrastructure, jobs, backups and retained data, integrations, model slots,
secret names, the complaint box, and the fleet conformance view with its
remediations and workers. A project's own conformance, deployments, targets,
retained data and secrets have project-scoped reads. `GET /api/v1/authz/me`
reports which instance reads the caller holds, so the web leaves pages the
caller cannot open out of the menu and shows "not available to you" when one
is reached by its address. That report is presentation, never the boundary.
A refusal on a project says "this project" and never its name, so guessing a
project's address reveals nothing about it.

## Rules every owner keeps

- Only a current instance-administrator grant confers root, and root reaches
  every project through one grant rule, after credential limits. Identity and
  grant administration require root. Root is exempt from approver eligibility
  and from the self-approval refusal; named approvers and author segregation
  still constrain everyone else.
- A token exercises root only when its ceiling permits it and its owner still
  holds root. OAuth and instrument bounds apply before root. Long-lived callers
  re-read grants. A command-line token made with no role takes its person's
  role at that moment as its ceiling; an agent token's is member. A refusal
  that the ceiling alone caused says so, so an administrator is not sent to
  an administrator.
- Initial setup creates authority explicitly. An empty administrator set grants
  nothing and never turns an absent caller into system work.
- A missing caller or missing provenance is an authentication failure, never a
  silent system fallback. An unverified caller cannot invoke ordinary commands.
  Anonymous callers act only through explicit read grants.
- One caller value carries identity, verification and receipt attribution
  separately. Attribution never grants authority; commands that take an actor
  check it equals the verified receipt actor.
- A failed policy read is an operational error, never an authorization answer
  (503 on HTTP). A route or tool guard, and a consequential owner check,
  records a denial in the activity log (`authz.denied`); a denial that
  cannot be recorded is a failure, not a refusal. An allowed decision is not
  stored: the act's own record carries who did it.
- A consequential act (changing who may do what, disclosing or changing a
  secret or credential, changing what runs, destroying work) writes its
  receipt in the act's transaction. An act outside the database records its
  request before and its outcome after; a secret is disclosed only after its
  receipt. An act inside an admitted run step records its outcome before the
  step passes. A line that only repeats an owner's own record is a note
  (`audit.BestEffort`) and may be lost.
- Creating a conformance exception requires `exception:approve`; there is no
  pending-request stage.
- Refusal comes before any record write, run admission or container start, and
  each operation's tests show it there. A shared kernel test does not replace
  them.
- The kernel is not a universal eligibility order: Work checks disabled
  accounts and approver eligibility before its approval capability,
  Conformance checks instrument scope before the human path, and Adoption
  keeps its receipt-actor equality check. General denial is `access.Denied`.

See [authentication and authority](auth.md) for modes and credential lifetime.

## Implementation references

- [internal/access/decision.go](../../internal/access/decision.go)
- [internal/apptown/identity/authority.go](../../internal/apptown/identity/authority.go)
- [internal/apptown/identity/visibility.go](../../internal/apptown/identity/visibility.go)
- [internal/apptown/usage/service.go](../../internal/apptown/usage/service.go)
- [internal/apptown/routes.go](../../internal/apptown/routes.go)
- [internal/apptown/mcpapi/catalog.go](../../internal/apptown/mcpapi/catalog.go)


The `instance:updates` capability is root-only. It admits authenticated release
status reads and explicit checks through the instance update owner, and is
projected in current holdings for the Updates page and notification. The owner
checks current caller authority; a hidden navigation item is not enforcement.
Periodic discovery has no installation or replacement authority.
