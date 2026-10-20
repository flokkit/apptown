# People, invites, roles and grants

Accounts are on the **People** page.

- **Invites.** With `-auth internal`, an administrator invites a person
  from **People → Invite a person** or with
  `apptown invite <username> [--role member|admin] [--days N]`. The link is
  shown once and works for 1 to 30 days (7 by default); the person sets
  their own password. AppTown sends no email.
- **Password reset links.** For an account that already has a password,
  an administrator uses **People → reset password** or
  `apptown password-link <username> [--days N]`. The link is shown once,
  works for 1 to 30 days (3 by default), and you hand it over yourself; a
  newer link for the same person withdraws the older one. Until it is used
  nothing changes. When the person uses it they choose a new password: the
  old password stops working and every browser session of theirs ends,
  while their roles and grants stay exactly as they were. This signs them
  out of browsers. Their personal access tokens and agent connections keep
  working — revoke those on the People page if the account may be
  compromised. A disabled account gets no link; enable it first. With
  single sign-on (`-auth trusted-header`) AppTown holds no passwords and
  refuses: reset the password in your identity provider.
- **Roles and grants.** A role is a named set of things a person may do.
  Roles on the whole AppTown (instance administrator, auditor, project
  creator and others) are granted on **People → Roles on this AppTown**;
  roles on one project are on its **Settings → Members**. A grant can name
  a person or a group. Nothing is implied: a power nobody was granted,
  nobody holds.
- **Who reads what, by default.** A new AppTown grants the built-in group
  *all authenticated users* the Auditor role on the whole AppTown: everyone
  signed in reads every project, change and record. It suits a team that
  works in the open. To keep projects private, revoke that grant on
  **People → Roles on this AppTown** (the row for *all authenticated users*,
  Auditor); then people read the projects they are members of. AppTown
  never puts the grant back. In open mode (`-auth open`, loopback only by
  default) anyone who can reach AppTown reads everything as well.
- **Tokens.** `apptown token create <name> --purpose cli|agent` makes a
  personal token. A `cli` token acts as its person; an `agent` token, for a
  coding agent, may not approve, land or administer.
- **Lost administrator password.** On AppTown's host, with its normal
  settings: `apptownd -reset-password <username>` prints a new password for
  that existing account, ends its sessions, and exits without serving.

[Connect a coding agent or the CLI](/docs/guides/connect-a-coding-agent)
covers tokens from a developer's side, and the
[glossary](/docs/guides/glossary) defines the roles' terms.
