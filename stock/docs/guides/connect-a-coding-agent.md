# Connect a coding agent or the CLI

<!-- A word like apptown:mcp in braces is a live link: the Docs page of an
AppTown shows that AppTown's own address there. Read apptown:origin as
https://<your AppTown host>, apptown:mcp as https://<your AppTown host>/mcp/user
and apptown:git as https://<your AppTown host>/git. -->

How to point a coding agent on your own computer — Claude Code, Codex, or
anything else that speaks MCP — at this AppTown, so it can see your projects
and Changes while you work, and how to set up the `apptown` command line.

## What you get

AppTown has one address for coding agents:

```
{apptown:mcp}
```

It speaks MCP (the Model Context Protocol — the standard way a coding agent
talks to tools outside its own editor). Once connected, the agent can ask
AppTown questions in plain terms: *which projects are there, what is
waiting on me, what did Change 42 do, did its checks pass?* — and get real
answers from the same records the web pages show.

The agent can also **do governed work in your name**: open a Change,
put AppTown's own agent on it, run its checks, boot its preview, hand it
off for approval, send it back with a note, or abandon it. It can ask for
a landing, but it cannot approve one or administer AppTown: a merge into
main or a deploy waits for a person's approval, or for a landing started by
someone allowed to land. It can also work in a checkout on your computer and
push the result to the Change.

## Step 1: tell your agent the address

That is the whole setup for a client that can sign in through a browser
(Claude Code and Codex both can). The first time the agent talks to the
AppTown, it opens your browser at AppTown's sign-in page. You sign in as
yourself, see exactly what the agent is asking for, and press **Allow**.
No token is copied anywhere: AppTown hands the agent its own short-lived
credential, bound to this one door, that stands for *you via that agent*.

### Claude Code

```
claude mcp add --transport http apptown {apptown:mcp}
```

Then in a Claude Code session, `/mcp` shows AppTown; choose it to sign
in. Afterwards it lists the tools it offers.

### Codex

Add this to `~/.codex/config.toml`:

```toml
[mcp_servers.apptown]
url = "{apptown:mcp}"
```

Run `codex mcp login forge` (or the equivalent your version offers; see
`codex mcp --help`) to open the browser sign-in.

### What you are allowing

The consent page says it in plain words. An agent asks for what it needs:
to **read** your projects, Changes, checks, evidence and runs, and, if it
is going to do work, to create projects, open Changes, put AppTown's own
agent to work on them, run checks, boot previews, hand off for approval,
and send a Change back or abandon it. Whatever it asks for, it cannot
approve or administer, and a landing it asks for still waits for a person,
whatever you yourself may do. It can never do more than you can.
You can disconnect any agent from **People → Connected coding agents**,
and an admin can disconnect anyone's. Disconnecting takes effect at once.

Your sign-in cookie never reaches the agent. Browser sign-in gives it an OAuth
credential restricted to the MCP door. The fallback agent token has the same
bounds wherever it is used. Git is a third door with its own short-lived credential,
minted per Change (`checkout_change`).

## Step 2 (fallback): a personal token

A client that cannot open a browser can use an **agent token**, a long
secret string that acts for you within the same bounds as browser sign-in.
Only an admin can make one:

```
apptown token create laptop-claude --purpose agent -u <your username>
```

The token is shown once. Copy it somewhere safe; it cannot be shown again.
If it leaks, an admin revokes it with `apptown token revoke laptop-claude`.

```
claude mcp add --transport http apptown {apptown:mcp} \
  --header "Authorization: Bearer <token>"
```

or, for Codex, `http_headers = { Authorization = "Bearer <token>" }` under
the same `[mcp_servers.apptown]` table. An agent token cannot approve or
administer, even on the plain API, and a landing it asks for waits for a
person. A command-line token (`--purpose cli`),
which acts as you, is refused here. Use browser sign-in when your client
supports it.

### Anything else

An MCP client that supports streamable HTTP can use the same endpoint: the
URL above, plus a header `Authorization: Bearer <token>`. AppTown answers
tool calls with plain JSON.

## What the agent can see

Twelve tools that read:

| Tool | What it returns |
|---|---|
| `list_projects` | the projects you can see, with how many Changes are open and where the main deployment runs |
| `get_project` | one project: its lanes (the workflows it can run), the guidance it adopted (names, versions and sizes), its conformance standing, its deployments |
| `get_project_guidance` | the adopted ways of working as one text, at the pinned versions AppTown's own agents are steered by. Offered and strongly recommended before writing code; never pushed into the agent's context, so the agent decides when to read it (the size is shown first) |
| `get_application_contract` | the project's committed `.apptown/app.yaml` — how the app runs |
| `list_changes` | a project's Changes, newest first, optionally only one state |
| `get_change` | one Change with its current run, every step's status, and what it is waiting on and on whom; while a person's approval of a commit waits, what that commit adds (see [How changes reach main](/docs/guides/changes-and-main)) |
| `get_change_diff` | the Change's whole diff as text |
| `get_change_evidence` | what the run recorded step by step, and the artifacts it produced |
| `get_run` | one run with its steps |
| `get_waiting_on_you` | the approvals waiting on you, each with what saying OK would mean |
| `get_what_if` | what a pack version's checks would say about projects' `main`, beside the checks they adopted; hypothetical, never a project's standing |
| `get_operation` | how a long operation stands (a data copy, a restore check, a take-down): queued, running, succeeded, failed, unknown or cancelled, with its steps; verification is a separate fact, and unknown means look before asking again |
| `get_eval` | an eval's scorecard: finished Changes re-run as their projects run now and with a model profile or pack version varied, compared finding by finding; read with your person's read of evals |

And nine that act, each recorded as *you, via the named agent*:

| Tool | What it does |
|---|---|
| `create_project` | creates a project in your name (slug, name, blurb, pack); the repository and the pack's starter files come with it |
| `create_change` | opens a Change with a title and a brief; with a lane named, AppTown's agent starts on it at once |
| `start_change_lane` | starts a lane (say, `implement`) on an open Change |
| `run_change_checks` | runs the Change's checks now — the same gates its landing will apply |
| `preview_change` | boots the Change and returns the URL to try it at |
| `request_change_landing` | hands the Change off: gates run, then the approvers see it |
| `send_change_back` | returns the Change to its agent with a note saying what to change |
| `abandon_change` | closes a Change that will not continue; nothing is deleted |
| `file_complaint` | tells AppTown about friction with AppTown itself (a tool that failed, a promise that did not hold); a person reads it |

And three for working in a local checkout — the agent edits on your
computer and AppTown takes the result the same way it takes its own
agents' work:

| Tool | What it does |
|---|---|
| `checkout_change` | returns the repository URL (`{apptown:git}/<project>`), the Change's branch and current tip, and a credential that lasts twenty minutes, opens only this Change's branch, and is used up by the first successful push |
| `renew_change_grant` | a fresh credential when the last one expired or was spent |
| `sync_change` | tells AppTown a revision was pushed; the Change's workspace adopts it, and the next steps (checks, preview, hand-off) are listed |

The push itself is ordinary `git push`. The credential goes in the
`APPTOWN_GIT_TICKET` environment variable and the returned commands read it
through a one-line credential helper, so it is never written into the
remote URL or `.git/config`. An agent whose shell tool starts a fresh
shell for every command (Claude Code does) must use the returned `shell`
form, which sets the variable inline on the same line; an `export` in an
earlier call does not carry over. The push lands in a holding ref named
by the credential; AppTown then moves the Change's branch to it, if that
credential is still the latest one for the Change. AppTown's repository
refuses anything else: pushing to main, another Change's branch, a rewind
of the branch, or a push from a tip that has since moved is turned away
with a one-line reason. Asking for a new credential retires the old one:
a push made with it is kept out of the branch, and `sync_change` reports
that the branch did not move. If AppTown's own agent is working on the Change, the
credential is refused until it finishes, so two writers never fight over
one branch.

Every answer carries a `webUrl` so the agent can hand you the page to
look at or act on. A tool the agent was not allowed says so in a sentence
naming what to ask for; it never fails silently.

The agent sees what you can see: AppTown checks your identity on every
call, and a Change or project it names by number is looked up and
checked, never taken on trust.

## The `apptown` command line

The `apptown` CLI talks to the same AppTown over its API. It needs AppTown's
address and a **command-line token**, which acts as you, with everything
you yourself may do. An admin makes one, on **People → API tokens** or with:

```
apptown token create laptop-cli --purpose cli -u <your username>
```

The token is shown once. Put it, with the address, in
`~/.config/apptown/credentials` (mode 0600), one `KEY=VALUE` per line:

```
APPTOWN_URL={apptown:origin}
APPTOWN_TOKEN=<token>
```

Environment variables of the same names win over the file. `apptown status`
then shows what is waiting on you. `APPTOWN_USER` names you only on an AppTown
in open mode, where names are attribution, not sign-in.

Keep the two kinds apart: a command-line token is refused at the MCP
address, and an agent token cannot approve on the command line. Revoke
either with `apptown token revoke <name>` or on the People page.

## What is recorded

Each time an agent connects, AppTown writes one line to the audit log:
who connected, which client (Claude Code, Codex, …) and its version. The
individual reads are not logged — they are reads. Every act — a Change
opened, a lane started, a hand-off — is recorded as *you, via the named
agent*, with the authorization decision behind it.

## If it does not connect

- **401, "sign in through your coding agent"** — the agent has no valid
  credential: it never signed in, you disconnected it, or its personal
  token was revoked. Sign in again from the agent (or check the header is
  exactly `Authorization: Bearer forge_…`).
- **"This AppTown runs in open mode"** — an open-mode AppTown has no identity
  to delegate. Switch it to internal auth (`-auth internal`) or SSO first.
- **403, "session user mismatch"** — the agent is presenting a session that
  belongs to another user. Restart the agent so it connects afresh.
- **404, "session not found"** — AppTown restarted or the session sat idle
  for a day. Clients start a new session on their own; if yours does not,
  restart it.
