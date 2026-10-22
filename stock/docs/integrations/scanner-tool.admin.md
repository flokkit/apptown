# Scanners — admin guide

## What it's for

Nothing ships without being checked for known
vulnerabilities in its dependencies, leaked secrets in its code, and
without a record of what is in it. Scanners are the tools that do that
checking; this guide is how to add, pin and tune them for the whole
instance.

A scanner is any container run against the committed tree — or, with
`target: image`, against the app's built container image — with the exit
contract: 0 = clean, 1 = findings (step fails), else error. Stock
manifests ship osv-scanner (dependencies), gitleaks (secrets), syft
(SBOM) on source, and trivy on the image; a preset exists for semgrep.

## Adding one

Integrations → Scanners → New integration (or a preset). Pin the image
tag — the config hash lands in run receipts. Declare the report path
and format (sarif/osv) to light up structured findings; leave network
off unless the tool queries a database (osv, semgrep --config auto).
Say what it looks at with `target: source` (default, the committed tree
at `/work`) or `target: image` (the built container, handed over as a
tarball — write `${IMAGE_TAR}` in the command). Test pulls the image
before you save.

Attaching is the whole act: the stock `land` and `ship` workflows carry
*scan points* (`on: source` in the gates, `on: image` before deploy)
that run every attached scanner with that target, each as its own check
with its own receipt. Nobody edits a workflow to add a scanner.

## How scans run

Scans execute against a staged `git archive HEAD` — the committed tree,
never the working directory — so AppTown's runtime state and agent scratch
can't pollute findings. Findings normalize into severity buckets;
verdicts are actionable = findings − active suppressions; suppressions
are VEX-shaped with justification and expiry, exportable as OpenVEX.

## What a landing's secret scan covers

At a landing, a source scanner that declares a `historyCommand` (stock
gitleaks does) reads every commit the landing will publish to main — the
commits between main as the landing checked it and the change's candidate,
including merges, each against its parents — not only the final tree.
AppTown first reads that range in the project's repository; a range it
cannot read is a scan error, never a clean pass. Each finding there is in
what main would gain, so it gates as found: it is not compared with main's
tree, where an older secret of the same kind in the same file would hide
it. Outside a landing, and for a scanner with no history command, only the
committed tree is read.

A secret committed and deleted later in the same change therefore fails
the landing: deleting it does not take it out of the history a merge
publishes. Rewrite the change's branch without the commit that added it,
or suppress the finding with a justification. AppTown reads the range
first because gitleaks reports a range its Git cannot read as clean; a
failure of the scanner's own Git after that, inside its container, is not
detected.

## Policy hooks

`scanner-bound` requires the capability bound; `deploy-requires-scan`
gates deploys on scans (`scan-failon-ceiling` ships too). The stock `land` workflow runs every
source scanner before the approval and every image scanner before the
deploy; `ship` does the same for lanes that call it. Every lane hardens
by attaching a scanner, not by editing a workflow.
