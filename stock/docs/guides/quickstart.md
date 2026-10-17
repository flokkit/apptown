# Start here

<!-- A word like forge:mcp in braces is a live link: the Docs page of a
forge shows that forge's own address there. -->

This page is for someone building an app on Forge. It shows the path a piece
of work takes and links to the guide for each step. Setting up a Forge itself is in
[Running a forge](/docs/guides/running-a-forge).

## Make something

1. Open **Projects → + New project**, choose a name and describe the app. Select a
   setup manually if you do not want to use the model-assisted interview.
2. Review the setup and services. Create a GitLab copy only if that integration is
   configured; a local project repository does not require GitLab.
3. Choose **Build the first version** to open a building chat. On an existing app,
   use **Change**. Confirm that the helper can answer before asking for a large task.
4. Use **Try it** to boot a preview. The selected source needs a valid
   `.forge/app.yaml`; a working process in the chat is not sufficient packaging.
5. Use **Check and land** and follow the checks and approval requested by the selected
   workflow. A successful landing updates main. Use **Open** once a deployment has
   an address. Deploying anywhere else needs a deployment target offered to the project.

The project's workflows, adopted guidance and policies determine which reviews
and scans run. Do not assume every Change uses the same gates.

## The path of a change

Every piece of work is a **change**: a branch off `main` with its own
record of who asked, what ran and who approved.

1. **Change.** Open one from the project's **Change** button, a chat, or
   `forge start`. Work on it in its chat, with a workflow (a fix, a planned
   change), or from your own computer.
   [How changes reach main](/docs/guides/changes-and-main) covers branches,
   staying current with `main`, and one worker at a time.
   [What chats and steps can use](/docs/guides/runtime-environments) covers
   the tools, where work runs and who can use a chat.
2. **Preview.** **Try it** boots the change cold from its `.forge/app.yaml`.
   The [contract reference](/docs/guides/contract-reference) says how to
   declare the app, [Services and data](/docs/guides/services-and-data) its
   database and other needs, [Secrets](/docs/guides/secrets) its
   credentials, and [Project toolchains](/docs/guides/toolchains) its tool
   versions. [How apps run](/docs/guides/how-apps-run) explains the
   difference between running it while you work and booting it cold.
3. **Land.** **Check and land** runs the project's gates and asks the
   approvers. Merging into `main` and deploying need a person's approval,
   or a landing started by someone allowed to land; the approval shows what
   the change adds.
   [How changes reach main](/docs/guides/changes-and-main) has the rules.
4. **Deploy.** A landing deploys to the project's `main` deployment. Other deployments, the kinds of
   target, taking a deployment down, and backups are in
   [Previews and deployments](/docs/guides/container-lifecycle).

[Dev loops](/docs/guides/dev-loops) describes the ways of working a change
supports, and the [glossary](/docs/guides/glossary) defines the terms.

## Connect your coding agent or the CLI

A coding agent on your own computer (Claude Code, Codex) can read your
projects and changes and do governed work in your name; the `forge` command
line does the same from a terminal. Both are set up in
[Connect a coding agent or the CLI](/docs/guides/connect-a-coding-agent).
On this forge, coding agents connect at {forge:mcp}.

## Set up a Forge

Installing and running a forge (its host, model access, workers, sign-in,
backups and upgrades) is in [Running a forge](/docs/guides/running-a-forge).
