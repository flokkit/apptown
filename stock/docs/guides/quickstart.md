# Start here

<!-- A word like apptown:mcp in braces is a live link: the Docs page of an
AppTown shows that AppTown's own address there. -->

This page is for someone making an app on AppTown. It shows the path a piece
of work takes and links to the guide for each step. Setting up AppTown itself
is in [Running AppTown](running-apptown.md).

## Make something

1. Open **Projects → + New project**, choose a name and describe the app. AppTown keeps the full request and
   suggests a short description; see [New project](new-project.md).
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
   [How changes reach main](changes-and-main.md) covers branches,
   catching up with `main`, and one worker at a time.
   [What chats and steps can use](runtime-environments.md) covers
   the tools, where work runs and who can use a chat.
2. **Preview.** **Try it** boots the change cold from its `.apptown/app.yaml`.
   [The app contract](contract-reference.md) says how to declare
   the app, [Services and data](services-and-data.md) its database
   and other needs, [Secrets](secrets.md) its credentials, and
   [Project toolchains](toolchains.md) its tool versions.
   [Running your app while you work](how-apps-run.md) explains
   the difference between running it as you work and booting it cold.
3. **Land.** **Check and land** runs the project's checks and asks the
   approvers. Merging into `main` and deploying need a person's approval, or
   a landing started by someone allowed to land; the approval shows what the
   change adds.
4. **Deploy.** A landing deploys to the project's `main` deployment. Other
   deployments, the kinds of target, taking a deployment down and backups
   are in [Previews and deployments](container-lifecycle.md).

The [glossary](glossary.md) defines the terms.

## Connect your coding agent or the command line

A coding agent on your own computer (Claude Code, Codex) can read your
projects and changes and do governed work in your name. The command-line
tool, called `apptown`, does the same from a terminal. Both are set up in
[Connect a coding agent or the CLI](connect-a-coding-agent.md).
On this AppTown, coding agents connect at {apptown:mcp}.
