# Scanners — developer guide

## What it's for

Before anything ships, the forge checks it for the
things you would not want to discover later: dependencies with known
security holes, a password or token committed by accident, and it
records exactly what is inside the build. A scan that finds something
stops the ship and shows you what and where.

Every landing runs every attached source scanner (dependencies,
secrets, SBOM by default) on the committed tree before the approval,
and every attached image scanner (trivy by default) on the public part's
image: pulled by its declared reference, or built from the landing's commit
(or found cached for that commit); failed scans block the deploy. The release
resolves its own images, so the scan's receipt names the image it scanned and
how it got it, not the release's digest, and lists the other parts and init
steps, which it does not scan. A public static site is skipped with a note
(its files are packed into the forge's file-server image at release), still
naming the other parts and init steps; an app with no `.forge/app.yaml`
passes the image point with a note.

An image scan compares with an earlier image: the image of the commit the
change started from, else the image the main deployment serves. On a
project's first release there is none, so the scan says "first release" and
every finding counts as new.

`forge check` runs the same source and image scans on your commit before you
hand back, as feedback: the landing still scans what it lands.

## When a scan blocks you

- The change page shows the findings under Details, on the scan's own
  check: grouped by package, with severity, fix versions where known,
  and the raw report as evidence.
- Fix it (bump the dep, remove the secret) — or, when a finding is
  genuinely not applicable, SUPPRESS it: per-finding, with a written
  justification and an expiry date. Blanket overrides are the wrong
  tool and the UI will push you toward per-finding suppressions.
- The project's Security tab has two views: Findings, the posture of
  what's on main with suppressions applied, and Suppressions, where
  they are made and expire.

## What "clean" means

A green scan attests the committed tree at that SHA, with the scanner
image + config hash pinned in the receipt. Reports are artifacts —
downloadable, diffable, and exportable (OpenVEX for suppressions,
CycloneDX for SBOMs).
