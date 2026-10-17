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
never the working directory — so forge runtime state and agent scratch
can't pollute findings. Findings normalize into severity buckets;
verdicts are actionable = findings − active suppressions; suppressions
are VEX-shaped with justification and expiry, exportable as OpenVEX.

## Policy hooks

`scanner-bound` requires the capability bound; `deploy-requires-scan`
gates deploys on scans (`scan-failon-ceiling` ships too). The stock `land` workflow runs every
source scanner before the approval and every image scanner before the
deploy; `ship` does the same for lanes that call it. Every lane hardens
by attaching a scanner, not by editing a workflow.
