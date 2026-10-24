# Scanners — developer guide

## What it's for

Before anything ships, AppTown checks it for the
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
steps, which it does not scan.

Each scanner says what it applies to (`appliesTo`, the words a check uses),
and a scan that applies to nothing the app declares says **not
applicable** with the reason, never a pass: the dependency scan (osv)
applies to an app with a part built with a language it reads (go, node,
python, ruby, rust, java, dotnet, php: `tools:` in `.apptown/app.yaml`);
secrets and the SBOM to any repository with files; the image scan to a
public part that runs as a container (a public static site is not
applicable: its files are packed into AppTown's file-server image at
release); the licence scan to a public container part built with node,
python, ruby or php. A scanner may declare `emptyExit`, the exit it gives
when it found nothing to judge (osv's 128, "no package sources found"):
where the scanner applies, that is a failure, since the contract says
there is something to scan. Commit the lockfile it reads (a .NET app its
packages.lock.json). A manual scanner, run only by a way of working's tool
check, takes that check's `appliesTo` and runs in each matching part's
folder, `${PART_FOLDER}` in its command.

An image scan compares with an earlier image: the image of the commit the
change forked from main at. When that image cannot be made, there is nothing
to compare with and every finding counts as new; the scan says why, and on a
project's first release it says "first release".

`apptown check` runs the same source and image scans on your commit before you
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
