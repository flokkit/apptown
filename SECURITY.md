# Security

## Reporting a vulnerability

Report it privately through GitHub's private vulnerability reporting: on
[github.com/flokkit/apptown](https://github.com/flokkit/apptown), open the
**Security** tab and choose **Report a vulnerability**. Please do not open a
public issue, pull request or discussion about it.

Include what you found, the version or commit, and how to reproduce it. The
maintainers will reply in the report. This is a small project run by its
maintainers in their own time: there is no promised response time, but every
report is read.

## Supported versions

Fixes go into the latest release (until 1.0, the latest release candidate).
Release notes are in [releases/](releases/).

## The trust model, in short

- **Open mode is for a local trial.** In the default `open` authentication
  mode the browser names the person it acts for, and AppTown believes it.
  Keep an open-mode AppTown on `127.0.0.1`; use `-auth internal` (or a trusted
  sign-in proxy) for anything shared.
- **The built-in secret store obfuscates; it is not a vault.** Anyone who can
  read AppTown's database and data directory can read its values. For a team,
  or any value that matters, use an external store
  ([secrets](stock/docs/guides/secrets.md)).
- **Containers are not a strong sandbox.** Agent, step and preview containers
  run as root on Linux, by choice, and can reach the network. A worker keeps
  project code off AppTown's own host; it does not make that code harmless
  ([split install](stock/docs/guides/split-install.md)).
- **Known threats and their status** are in the
  [threat register](design/architecture/threat-register.md): each entry says
  whether it is mitigated, partly mitigated, accepted or open.
