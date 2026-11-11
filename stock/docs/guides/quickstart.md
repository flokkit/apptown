# Start here

<!-- A word like apptown:mcp in braces is a live link: the Docs page of an
AppTown shows that AppTown's own address there. -->

This page is for someone making an app on AppTown. It shows the path a piece
of work takes and links to the guide for each step. Setting up AppTown itself
is in [Running AppTown](running-apptown.md).

## Make something

1. Open **Projects → + Create project**.
2. Choose a name and describe the app. AppTown keeps the full request and
   suggests a short description. See [Create project](new-project.md).
   To skip the interview with a model, choose a setup by hand.
3. Review the setup and services. A GitLab copy is made only when that
   integration is set up. A project needs no GitLab.
4. Choose **Build the first version** to open a building chat. On an
   existing app, use **Change** instead.
5. Check the helper answers before you ask for a large task.
6. Use **Try it** to boot a preview. The commit needs a valid
   `.apptown/app.yaml`. A process running in the chat is not packaging.
7. Use **Check and land**. Follow the checks and approval the project's
   workflow asks for.
8. Use **Open** once the deployment has an address.

A landing updates `main` and builds its bundle. Deployments that follow main
request that bundle afterwards. Deploying anywhere else needs a deployment
target offered to the project.

The project's workflows, guidance and policies decide which reviews and
scans run. So two projects can ask for different things.

## The path of a change

Every piece of work is a **change**. A change is a branch off `main` with its
own record of who asked, what ran and who approved. It goes through four
steps.

### 1. Change

Open a change from the project's **Change** button, a chat, or
`apptown start`. Then work on it in one of these ways:

- in its chat;
- with a workflow, such as fix a bug or a planned change;
- from your own computer.

More:

- [How changes reach main](changes-and-main.md): branches, catching up with
  `main`, and one worker at a time.
- [What chats and steps can use](runtime-environments.md): the tools, where
  work runs and who can use a chat.

### 2. Preview

**Try it** boots the change cold from its `.apptown/app.yaml`.

- [The app contract](contract-reference.md): how to declare the app.
- [Services and data](services-and-data.md): its database and other needs.
- [Secrets](secrets.md): its credentials.
- [Project toolchains](toolchains.md): its tool versions.
- [Running your app while you work](how-apps-run.md): running it as you work
  compared with booting it cold.

### 3. Land

**Check and land** runs the project's checks and asks the approvers.

Merging into `main` needs one of these:

- a person's approval;
- a landing started by someone allowed to land.

The approval shows what the change adds.

### 4. Deploy

A landing builds its runtime bundle. Deployments configured to follow main
request that bundle after landing completes.

[Previews and deployments](container-lifecycle.md) covers other
deployments, the kinds of target, taking a deployment down and backups.

The [glossary](glossary.md) defines the terms.

## Connect your coding agent or the command line

A coding agent on your own computer (Claude Code, Codex) can read your
projects and changes and do governed work in your name. The command-line
tool, called `apptown`, does the same from a terminal. Both are set up in
[Connect a coding agent or the CLI](connect-a-coding-agent.md).
On this AppTown, coding agents connect at {apptown:mcp}.
