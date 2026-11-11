# Images: where they live and how long they're kept

An image contains the code and files a release runs. A release records each
image by its digest, so moving a tag does not change what that release runs.

## The hub registry

You manage the hub, AppTown manages the spokes.

An administrator sets up one **hub registry** under Integrations. Its credential
must read and write every project's repository. AppTown derives each repository
as `<registry host>/<repository prefix>/<project slug>`. Projects do not attach
registries or create their own registry credentials.

AppTown keeps images from the last 90 days and the newest 10 release bundles per
project by default. An administrator can change both numbers on the hub's
integration. AppTown also keeps images needed by retained target copies, kept
backups, and releases waiting for approval or execution. A digest shared by a
kept bundle and an expired bundle stays.

AppTown applies this policy daily at 03:30, after the backup sweep. The hub's
Integrations row and each target page show the last pass. The Jobs page shows
the scheduled pass; the audit record lists what it deleted, left, or could not
check. If AppTown cannot read the keep sets, it deletes nothing.

Deleting a manifest does not itself reclaim its stored layers. You configure
the hub registry's garbage collection. A registry that refuses deletion keeps
its space; AppTown reports that refusal. Registry-side lifecycle policies are
yours to manage. AppTown does not read them.

## Copies where apps run

A **spoke** is the image store where an app runs. AppTown keeps its running
release, the last three successful releases, every successful release from the
last seven days, and any release a backup AppTown still keeps was taken with.
AppTown combines keep sets for deployments sharing one store. It removes only
images recorded in release bundles and its own unused landing build tags.
Images that containers still use stay.
Removing unused images from a Docker host yourself (`docker image prune -a`) is
fine: AppTown notices on its next reading of the host, forgets those copies
without reporting them missing, and pulls one again when a release needs it.
If AppTown cannot identify one target's image store in a pass (a Docker host or
Portainer endpoint that does not answer, an async edge endpoint), it deletes
from no target that pass, because that store might be shared with another.

| Target | Where its images live | What frees the space |
| --- | --- | --- |
| Docker host | The Docker daemon's image store | AppTown removes unused recorded images and landing build tags without forcing removal. |
| Portainer | The endpoint's Docker image store | The same removal through Portainer's Docker proxy. Async edge endpoints cannot be reached, so while a deployment runs on one no target's images are removed. |
| DigitalOcean App Platform | The project's repository in DOCR | AppTown deletes expired manifests, then requests garbage collection when no App Platform release is in flight. DOCR refuses pushes while collection runs. |
| AWS ECS on Fargate | The deployment's repository in ECR | AppTown deletes expired images by digest. The AWS credential needs `ecr:BatchDeleteImage`; update the setup policy for an existing target. |
| Fly | The deployment app's repository in Fly's registry | AppTown requests manifest deletion. If Fly refuses, the space stays until the app is taken down. |
| Bucket website | No image copy; the bucket holds files | There are no spoke images to remove. Each release reads its files from the hub. |

A retained spoke image can restart or roll back while the hub is unreachable.
Docker checks that it holds the recorded digest. Portainer pulls only missing
images on stack updates. Cloud registries confirm the retained digest and its
platform before reusing it. A bucket website's rollback needs the hub.

## When an old image is gone

When the hub confirms that an image is missing and the spoke does not hold it,
AppTown rebuilds from the release's recorded commit. A new build run produces a
new bundle and then resumes the accepted release request on the asking deployment.
Current authority and pre-release backups still apply. A rebuilt image needs fresh
approval when the deployment requires approval.
The new release says **rebuilt from commit, not the original image**. Dependencies
and build tools may have changed, so rebuilding does not recover identical bytes.
If the rebuild's build fails, the release fails with the build's reason and the
deployment is free for the next release.

An unreachable hub or a refused credential fails the release. Neither starts a
rebuild. If there is no hub, releases built without a worker hold image IDs on
their Docker host; an older image removed there is rebuilt from its commit.

When moving the hub, recorded bundles keep their old repository. AppTown gives
the hub credential only to images on the current registry host. A private image
on another host fails with that registry's answer; an administrator must resolve
access. It is not treated as a missing image.
