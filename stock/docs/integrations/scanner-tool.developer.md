# Scanners — developer guide

## What it's for

Before anything ships, AppTown checks it for things you would not want to
discover later:

- dependencies with known security holes;
- a password or token committed by accident.

It also records exactly what is inside the build. A scan that finds
something blocks the landing and shows you what it found and where.

## What runs at a landing

Every landing runs each attached source and image scanner that is not
manual. A failed scan blocks the landing.

A manual scanner runs only when a way of working's tool check names it.
Attaching one to a project does not make it run at a landing.

- **Source scanners** read the committed files before the approval. By
  default these scan dependencies and secrets, and make the software
  inventory (SBOM).
- **Image scanners** read the public part's image. The default is trivy.
  AppTown pulls the image by its declared reference, builds it from the
  landing's commit, or finds it cached for that commit.

The release resolves its own images. So the scan's receipt names the image
it scanned and how it got it, not the release's digest. The receipt also
lists the other parts and init steps, which the scan does not read.

## What each scanner applies to

Each scanner says what it applies to (`appliesTo`, the words a check uses).
A scan that applies to nothing the app declares says **not applicable**,
with the reason. It is never a pass.

- **Dependency scan (osv):** an app with a part built with a language it
  reads. These are go, node, python, ruby, rust, java, dotnet and php, set
  by `tools:` in `.apptown/app.yaml`.
- **Secrets and the SBOM:** any repository with files.
- **Image scan:** a public part that runs as a container. A public static
  site is not applicable. Its files are packed into AppTown's file-server
  image at release.
- **Licence scan:** a public container part built with node, python, ruby
  or php.

A scanner that applies but finds nothing to read fails. For osv this is
"no package sources found". Commit the lockfile it reads. For a .NET app
that is `packages.lock.json`. The [admin guide](scanner-tool.admin.md) says
how a scanner declares this, and how manual scanners choose their folders.

## What an image scan compares with

An image scan compares with an earlier image: the image of the commit where
the change forked from main.

When that image cannot be made, there is nothing to compare with. Every
finding then counts as new, and the scan says why. On a project's first
release it says "first release".

## Scanning before you hand back

`apptown check` runs the same source and image scans on your commit. This
is feedback only. The landing still scans what it lands.

## When a scan blocks you

The change page shows the findings under **Details**, on the scan's own
check. They are grouped by package, with:

- severity;
- fix versions, where known;
- the raw report, as evidence.

Then do one of these:

- **Fix it.** Update the dependency or remove the secret.
- **Suppress it,** when a finding genuinely does not apply. A suppression
  covers one finding. It needs a written justification and an expiry date.

Blanket overrides are the wrong tool. The page steers you to suppressing
single findings instead.

The project's **Security** tab has two views:

- **Findings:** what is on main now, with suppressions applied.
- **Suppressions:** where suppressions are made and where they expire.

## What "clean" means

A passing scan covers the files committed at that exact commit. The
receipt records the scanner image and its config hash, a fingerprint of its
settings.

Reports are kept as records. You can download them, compare them and export
them: suppressions as OpenVEX, software inventories as CycloneDX.
