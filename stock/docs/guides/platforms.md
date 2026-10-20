# Supported platforms and system requirements

What AppTown runs on, and what it needs. "Supported" means tested for this
release; "experimental" means it may work and is not yet tested.

## AppTown on a server

| | Supported |
| --- | --- |
| Operating system | Linux on amd64 or arm64 |
| Container runtime | Docker Engine |
| Database | the PostgreSQL AppTown starts for itself, or your own |

One AppTown uses one CPU architecture for everything it builds and runs:
its workers and deployment targets run the same one (the [app
contract](/docs/guides/contract-reference), "One architecture").

## The desktop trial

| | Supported | Experimental | Not yet |
| --- | --- | --- | --- |
| macOS | Apple Silicon with Docker Desktop | Intel Macs | |
| Linux | Docker Engine | | |
| Windows | | | planned for 1.1 |

The trial serves its apps at `https://….localhost` addresses. Chrome,
Edge and Firefox reach those names on every supported system. Safari
reaches them only from macOS 26; on an older macOS use Chrome or Firefox for
the trial. The trial's certificate is trusted once per computer ([Running
AppTown](/docs/guides/running-apptown), "Trust the trial's certificate").

## Browsers

The current versions of Chrome, Edge, Firefox and Safari.

## Deployment targets

A Docker host, built in or attached over SSH, is supported. Portainer,
DigitalOcean App Platform and bucket websites are experimental ([What 1.0
promises](/docs/guides/compatibility)).

## System requirements

Measured with one agent at work:

- AppTown itself uses about 1.9 GB of memory.
- Each agent session may use up to 4 GB of memory, and up to 4 GB more for
  its private Docker.
- The agent image is 2.5 GB on disk, and one project with one change came
  to 9.8 GB of data.

So:

| | CPUs | Memory | Free disk |
| --- | --- | --- | --- |
| Desktop trial | 4 | at least 8 GB given to Docker; 16 GB recommended | 30 GB |
| Server | 4 | 8 GB; 16 GB recommended | 30 GB |

A server needs, besides, what its apps use: their memory and disk, and
their data's backups.
