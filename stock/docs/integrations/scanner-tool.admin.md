# Scanners — admin guide

## What it's for

Nothing ships until AppTown has checked it. It looks for known
vulnerabilities in the app's dependencies and for secrets leaked in its code.
It also records what is in the app. Scanners are the tools that do that
checking. This guide says how to add, pin and tune them for the whole
instance.

## What a scanner is

A scanner is any container AppTown runs against the app. It reads one of
two things:

- With `target: source`, the committed files.
- With `target: image`, the app's built container image.

Its exit code says what it found:

- 0: clean.
- 1: findings. The step fails.
- Any other code: an error.

The stock scanners are:

- osv-scanner, for dependencies, on source.
- gitleaks, for secrets, on source.
- syft, for the software inventory (SBOM), on source.
- trivy, on the image.

A preset exists for semgrep.

## Adding one

1. Open **Integrations → Scanners → New integration**, or choose a preset.
2. Pin the image tag. Run receipts record the scanner's config hash, a
   fingerprint of its settings.
3. Declare the report path and its format (`sarif` or `osv`). AppTown then
   shows structured findings.
4. Leave network off unless the tool queries a database (osv,
   `semgrep --config auto`).
5. Say what it looks at:
   - `target: source` is the default. The scanner reads the committed files
     at `/work`.
   - `target: image` hands the built container over as a tarball. Write
     `${IMAGE_TAR}` in the command.
6. Choose **Test**. It pulls the image before you save.

Attaching is the whole act. The stock `land` workflow has scan points in
its gates: `on: source` and `on: image`. Each scan point runs every attached
scanner with that target. Each scanner is its own check, with its own
receipt. Nobody edits a workflow to add a scanner.

## What a scanner applies to

Each scanner says what it applies to in `appliesTo`, using the words a check
uses. The [developer guide](scanner-tool.developer.md) lists what each
stock scanner applies to.

A scanner may declare `emptyExit`. This is the exit code it gives when it
found nothing to judge. For osv it is 128, "no package sources found". Where
the scanner applies, that exit is a failure, because the app declares
something to scan.

A manual scanner runs only from a way of working's tool check. It takes that
check's `appliesTo`. It runs in the folder of each matching part. Write
`${PART_FOLDER}` in its command for that folder.

## How scans run

- Scans read a staged `git archive HEAD`. That is the committed files, never
  the working directory. So AppTown's runtime files and agents' scratch files
  cannot add findings.
- AppTown sorts findings by severity.
- A finding counts unless an active suppression covers it.
- A suppression has a written justification and an expiry date.
- Suppressions can be exported as OpenVEX.

## What a landing's secret scan covers

At a landing, some source scanners read history, not only the final files.
These are scanners that declare a `historyCommand`. Stock gitleaks does.

Such a scanner reads every commit the landing will publish to main:

- the commits between main, as the landing checked it, and the change's
  candidate;
- merges included, each read against its parents.

AppTown first reads that range in the project's repository. If it cannot
read the range, the scan is an error, never a clean pass.

Each finding in that range is in what main would gain, so it blocks as
found. It is not compared with main's files. There, an older secret of the
same kind in the same file would hide it.

Outside a landing, only the committed files are read. The same is true for a
scanner with no history command.

A secret committed and then deleted in the same change still fails the
landing. Deleting it does not take it out of the history a merge publishes.
To fix it, do one of these:

- Rewrite the change's branch without the commit that added the secret.
- Suppress the finding, with a justification.

AppTown reads the range first because gitleaks reports a range its Git
cannot read as clean. After that first read, a failure of the scanner's own
Git inside its container is not detected.

## Policies

- `scanner-bound` requires a working scanner integration.
- `build-requires-scan` requires a scan in a stage before a build.
- `scan-failon-ceiling` also ships. It limits how loose a scan's `failOn`
  may be set.

The stock `land` workflow runs every source and image scanner before
approval. Image scanners pull or build the image they check.

After merging, the landing builds the runtime bundle. Deployments that
follow main request that bundle separately.

Attach a scanner to include it in landings. No workflow edit is needed.
