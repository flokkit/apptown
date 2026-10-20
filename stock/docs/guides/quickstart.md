# Start here

<!-- A word like apptown:mcp in braces is a live link: the Docs page of an
AppTown shows that AppTown's own address there. -->

This page is for someone making an app on AppTown. It shows the path a piece
of work takes and links to the guide for each step. Setting up AppTown itself
is in [Running AppTown](/docs/guides/running-apptown).

## Make something

1. Open **Projects → + New project**, choose a name and describe the app.
   Choose a setup by hand if you do not want the interview with a model.
2. Review the setup and services. A GitLab copy is made only when that
   integration is set up; a project needs no GitLab.
3. Choose **Build the first version** to open a building chat. On an
   existing app, use **Change**. Check the helper answers before you ask for
   a large task.
4. Use **Try it** to boot a preview. The commit needs a valid
   `.apptown/app.yaml`: a process running in the chat is not packaging.
5. Use **Check and land**, and follow the checks and approval the project's
   workflow asks for. A landing updates `main` and deploys it. Use **Open**
   once the deployment has an address. Deploying anywhere else needs a
   deployment target offered to the project.

The project's workflows, guidance and policies decide which reviews and
scans run, so two projects can ask for different things.

## The path of a change

Every piece of work is a **change**: a branch off `main` with its own record
of who asked, what ran and who approved.

1. **Change.** Open one from the project's **Change** button, a chat, or
   `apptown start`. Work on it in its chat, with a workflow (fix a bug, a
   planned change), or from your own computer.
   [How changes reach main](/docs/guides/changes-and-main) covers branches,
   catching up with `main`, and one worker at a time.
   [What chats and steps can use](/docs/guides/runtime-environments) covers
   the tools, where work runs and who can use a chat.
2. **Preview.** **Try it** boots the change cold from its `.apptown/app.yaml`.
   [The app contract](/docs/guides/contract-reference) says how to declare
   the app, [Services and data](/docs/guides/services-and-data) its database
   and other needs, [Secrets](/docs/guides/secrets) its credentials, and
   [Project toolchains](/docs/guides/toolchains) its tool versions.
   [Running your app while you work](/docs/guides/how-apps-run) explains
   the difference between running it as you work and booting it cold.
3. **Land.** **Check and land** runs the project's checks and asks the
   approvers. Merging into `main` and deploying need a person's approval, or
   a landing started by someone allowed to land; the approval shows what the
   change adds.
4. **Deploy.** A landing deploys to the project's `main` deployment. Other
   deployments, the kinds of target, taking a deployment down and backups
   are in [Previews and deployments](/docs/guides/container-lifecycle).

The [glossary](/docs/guides/glossary) defines the terms.

## Connect your coding agent or the command line

A coding agent on your own computer (Claude Code, Codex) can read your
projects and changes and do governed work in your name. The command-line
tool, called `apptown`, does the same from a terminal. Both are set up in
[Connect a coding agent or the CLI](/docs/guides/connect-a-coding-agent).
On this AppTown, coding agents connect at {apptown:mcp}.
