# Starting a project from existing code, and moving one to another AppTown

A new project's repository can begin with the history of existing code.
**Only the code comes**: its default branch and its tags. Changes, runs,
chats, approvals and the ledger start fresh on the new project. Nothing
from the source's records is imported.

## From a Git bundle

```sh
apptown new <slug> --from code.bundle [--branch B] [--pack P] [--toolchain T]
```

The default branch is chosen in this order:

1. `--branch`, if given.
2. The branch the bundle's `HEAD` names.
3. The bundle's only branch.

A bundle with several branches and no `HEAD` is refused, and the refusal
lists its branches.

Make a bundle from any repository with:

```sh
git bundle create code.bundle main --tags
```

An incremental bundle is refused, because it needs history it does not
carry. So is a damaged one. A refused bundle leaves no repository behind.

The largest bundle AppTown accepts is set by the operator with
`-import-bundle-max-bytes`. The default is 2 GiB. The same limit applies to
each push to AppTown's Git address; a larger push is refused with HTTP 413.

## From an https repository

```sh
apptown new <slug> --from https://gitlab.example.com/team/app.git [--secret-for-git GITLAB_TOKEN]
```

AppTown's server does the fetch. To limit where it can be sent, it
fetches only under these conditions:

- The URL is https on port 443, with no user name or password in it.
- Every address the host resolves to is public. Loopback, private
  networks, link-local addresses (including cloud metadata), carrier-grade
  NAT, unspecified and multicast addresses are all refused.
  - An operator who needs an internal server (an internal GitLab, say)
    starts apptownd with `-git-import-allow-private`. That allows **every**
    private address, not just one.
- Git is pinned to the address that was checked, and redirects are off.

A failed fetch is reported in AppTown's own words: whether access was
refused, nothing was found at the address, or the server could not be
reached. Git's messages are not passed on, because a server's error text
can repeat the credential it was given. One fetch may take at most ten
minutes.

`--secret-for-git` names one of AppTown's own secrets, set with
`apptown secret set` or under Integrations › Secrets. AppTown offers its
value as the git password, with the user name `apptown`. Only an
administrator may name one, because the value goes to whatever server the
URL names.

## What happens after the code is in

The repository is set up as any new project's is:

- If the code has no `.gitignore`, AppTown adds one as a commit. The new
  project's `main` is then one commit ahead of the source.
- `--pack` skips files the code already has.
- `--toolchain` refuses if `mise.toml` exists.

Symbolic links in the code stay links. AppTown never writes through one
when it sets the project up. If a pack's starter file would have to pass
through a link, AppTown skips that file and the pack's answer names it.
A `.gitignore` that is a link counts as the project's own and is left
alone.

AppTown reads `mise.toml`, `.apptown/app.yaml` and `secretspec.toml` from
the tree, as it does for any project. The command lists the secret names
`secretspec.toml` declares. Set them in Settings › Secrets: secret values
never travel with code.

Starting from code is `project:import` on the new project. The person who
created the project holds it. A connected agent with read-only consent
does not, and neither does a chat session.

## Moving a project to another AppTown

On the source AppTown, anyone who can read the project can run:

```sh
apptown bundle -p <slug> > <slug>.bundle
```

Alternatively, use the **Download code** button under Settings › General ›
Repository. The bundle holds the default branch, its tags, and a `HEAD` that
names the default.

On the destination AppTown:

```sh
apptown new <slug> --from <slug>.bundle [--pack P]
```

Then set the secrets the command lists. Attach the integrations and
deployment targets the project needs. The project's history on the source
AppTown stays there. When you no longer need the old project, delete it on
the source.

## When something fails

A seed that fails leaves the project created, with no repository. Delete
it (`apptown delete <slug>`) and run `apptown new --from` again. A project
that already has a repository is never seeded again, so an existing
history cannot be replaced.
