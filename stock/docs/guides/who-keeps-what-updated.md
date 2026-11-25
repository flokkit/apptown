# Who keeps what up to date

Each thing an app runs on has someone who keeps it current, someone who decides
bigger moves, and a place where a person hears when it is stuck.

| What the app runs on | Kept up to date by | Bigger moves decided by | You hear about it in |
| --- | --- | --- | --- |
| App dependencies | The [Janitor](janitor.md), within each release line (a tracked product's cycle such as Django 5.2, otherwise its major) | The project owner, by a change; the Janitor too when its level is Including major and another line is due | Maintenance › Needs you |
| Base images in the project's Dockerfiles | The Janitor, within each image's release line (PHP 8.3, Python 3.12, Node 24), keeping its name and variant | Another release line: the Janitor at Including major when it is due; another image or variant: the project owner, by a change | Maintenance › Needs you |
| Static server (Caddy 2 on Alpine) | AppTown, when there is no `staticServer:` pin: each build uses the newest `caddy:2-alpine` it reads; the Janitor moves an existing pin ([app contract](contract-reference.md)) | The project owner, by adding or changing a pin | Maintenance › Needs you |
| Prebuilt images an `app:` part runs (`image:`) | Nobody automatically | The project owner, by a change | The project's Security tab, for the public part |
| Language toolchains (`mise.toml`) | mise installs what is declared: a line such as `node = "24"` gets its newest release, an exact pin stays; the Janitor moves a pin only for a patch a security finding needs | The project owner, by a change | Maintenance › Needs you |
| OS packages in release images | Each release build, where the Dockerfile's final stage upgrades packages; the Janitor rebuilds a deployment following main when its running image shows a new fixable finding ([Janitor](janitor.md)) | The project owner | The project's Security tab |
| Postgres that AppTown runs on a Docker host | AppTown, as releases: each bundle records the newest build on the declared major line; the Janitor requests a refresh when it is behind. An exact version stays pinned. Deploy this again on an earlier release returns its recorded build on the same major line; Roll back to before a release also restores its data copy. Automatic routine updates can refresh it again on the next daily scan; pin `postgres: {version: "16.15"}` or set routine updates to ask to stay back | The project owner, by a migration to another major line | The deployment, Maintenance and Security |
| MariaDB and Redis that AppTown runs on a Docker host | Nobody automatically: a standing service keeps the image it started with, on the line the project declares (default lines are fixed for every 1.x release) | The project owner; a changed version is refused where the service holds real data, so moving one is a person's migration ([Services and data](services-and-data.md)) | The deployment |
| Object storage that AppTown runs on a Docker host | The AppTown release, at the deployment's next release after an upgrade | The administrator, by upgrading AppTown | The deployment |
| Managed databases and buckets a cloud provider runs | The provider; AppTown records the major version it asked for, not the provider's patch level | The project owner (the declared version) and the provider | The provider |
| [Shared services](glossary.md) your organisation runs | Your organisation; AppTown does not run them | Your organisation | Your organisation |
| Ways of working (packs) | Pack authors publish versions (stock packs come with AppTown releases); the Janitor's Keep packs current adopts them | The project owner, after a [what-if](packs-and-what-if.md) | Maintenance › Needs you |
| AppTown itself | The administrator installs releases ([Upgrading](upgrading.md)) | The administrator | Updates |
| Scanners | Scanner images come with the AppTown release; vulnerability data is read at each scan | The administrator, by upgrading AppTown | The project's Security tab |
| Hosts, disks and Docker | The administrator, or the cloud provider for its machines | The administrator | Attention |
| A vulnerability with no fixed release | Upstream; a source dependency's is listed with an empty Fix column, and the image scan leaves out findings with no fix until one is published | The Janitor within its level once a fix ships; the static server only through routine updates of its pin | The project's Security tab |

The Janitor starts on at patch and minor (within each release line), with security fixes, base images,
toolchain patches and packs; Maintenance › Settings changes
that for every project.
Each project can choose its own level or turn the Janitor off in Settings ›
General, and each deployment chooses in its Settings tab whether releases go
out automatically, when a person says, or in a release window.
