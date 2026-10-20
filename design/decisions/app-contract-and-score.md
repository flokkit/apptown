# The app contract is AppTown's own; Score is watched

AppTown's app contract is `.apptown/app.yaml`
([the app contract](../../stock/docs/guides/contract-reference.md)), with the
needs catalogue behind it ([Services and data](../../stock/docs/guides/services-and-data.md)).
It is AppTown's own format, and 1.x keeps it stable. [Score](https://score.dev)
is the nearest standard: a workload declares containers and the resources it
needs, and each platform's provisioners decide how to provide them. AppTown
does not adopt Score as its contract, and does not read or write it.

## What AppTown took from Score

- **Kubernetes units.** Memory is `Mi` or `Gi`, and cpu a number or
  millicores (`500m`), so AppTown's numbers mean what they mean in
  Kubernetes and Score.
- **Explicit wiring, as an option.** An environment value may read a need's
  value, `${needs.<need>.<VARIABLE>}`, for an image that expects its own
  names. The fixed variables each need gives stay the default.

## Why not Score as the contract

- **A need is one contract on every target.** AppTown fixes which variables
  each need gives and what an app may rely on, the same everywhere. Score
  leaves resource types and their outputs to each platform, so an app wires
  every value by hand and moves between platforms only as far as their
  provisioners agree.
- **Half of AppTown's contract has no Score field**: building from source,
  static sites, the one public part, profiles, secrets, health timings,
  restore rehearsal checks. Score's only open slot is free-form metadata,
  which no Score tool checks.
- **Parts are separate services.** A Score file is one workload whose
  containers share `localhost`; AppTown's parts are reached by name, each with
  its own data folder and restarts, and init steps run before every part.
- **The 1.x promise stays AppTown's.** Score's spec is a beta version whose
  shape is controlled elsewhere.

## What would change this

AppTown would add a Score importer or exporter, or adopt Score, when these
hold:

- a stable Score v1 spec;
- hosting platforms other than Score's own tools reading `score.yaml`;
- resource types and their outputs standardised across implementations;
- maintainers from several organisations.

Context on 20 October 2026, from a research note and a trial conversion:
the spec was `score.dev/v1b1`, a CNCF Sandbox project since July 2024; its
maintainers had all moved from Humanitec, its sponsor, to other employers;
the reference tools (score-compose, score-k8s) were released often, largely
by one maintainer; one independent production user was documented. A
WordPress app converted by hand ran on score-compose only with a provisioner
written for it. An importer or exporter would not change AppTown's contract.
