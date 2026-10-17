# What Forge is for

Forge (product name Flokkit) is the home for a team's internal software and the
changes made to it. Coding agents make writing code cheap; owning software stays
expensive. Forge exists for four problems that agents make worse:

- **Work nobody can audit.** Who asked for a change, which rules applied, what
  was checked, who accepted it, what is running now.
- **Apps that all work differently.** Each one built, deployed and backed up its
  own way, so every app is a new thing to learn and look after.
- **Knowledge that does not steer the work.** A team's conventions live in
  people's heads and wikis, not where an agent will follow them.
- **More code than people can read.** Reviewing every diff does not scale when
  agents write most of it.

## What it does

A person, or a coding agent on their behalf, asks for a **change** to an app.
Forge runs the change through the team's **way of working**: agents do the work
in containers, with the isolation limits that "Where work runs" in
[How the forge works](/docs/guides/architecture) states; checks run, and a
person approves where the rules say a person must. Forge then lands the
change on the app's main branch and deploys it.

Every step leaves a record: who asked, which rules governed the work (and at
which version), what each check found, who approved and what their approval
meant, and what is running where. Anyone with access can follow a change from
the request to the running app.

A team's way of working is **data**: guidance for agents, workflows, policies,
checks and approvals, versioned and adopted per project. Changing how the team
works is a change to that data, not to Forge.

Apps describe what they need in one small file, `.forge/app.yaml`, and Forge
runs them on a Docker host, Portainer, DigitalOcean App Platform or a bucket
website. Secrets, services and error reporting are declared and wired the same
way for every app. What each target can do differs: Forge backs up an app's
data on a Docker host and in a Portainer environment, and copies a managed
database and a bucket on App Platform (with a copy runner and a backup store); [Backups and restore](/docs/guides/container-lifecycle)
says what each target gets.

## What it is not

Forge is not a general agent-orchestration framework, a replacement for your
Git host or ticket tracker, a low-code platform, or an attempt at maximum
autonomy. The software it produces is ordinary software in ordinary
repositories: you can take it elsewhere.

## Read next

- [Quickstart](/docs/guides/quickstart): run a forge and make a first change.
- [How the forge works](/docs/guides/architecture): the pieces and how a change moves.
- [What 1.0 promises](/docs/guides/compatibility) and [what is next](/docs/guides/roadmap).
