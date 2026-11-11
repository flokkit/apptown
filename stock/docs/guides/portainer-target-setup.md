# Portainer target setup

A Portainer target runs each deployment as a stack in an environment of an
existing Portainer. AppTown talks only to Portainer's API; it never connects
to the Docker machine itself. This target is experimental: no real app has
been qualified on it yet.

## What you need

- The **Portainer targets** feature turned on under **Setup → Features**
  ([Features](features.md)).
- A Portainer that AppTown can reach over HTTP or HTTPS.
- In it, a standalone Docker environment, reached directly or through the
  Portainer agent. Edge, Kubernetes and Swarm environments are refused.
- For apps at a domain: a wildcard DNS record such as `*.apps.example.com`
  pointing at the Docker machine, and Traefik running there. AppTown labels
  each deployment's public container for Traefik; it does not install or run
  Traefik. [AppTown setups](setups.md) lists what Traefik needs.

## The credential

In Portainer, sign in as a user who can see the environment and manage its
stacks, and create an access token under **My account › Access tokens**.
AppTown does not check the user's role; a token whose user cannot manage
stacks fails at the first release.

Store two AppTown secrets, under **Integrations › Secrets** or with
`apptown secret set`:

- the Portainer address, such as `https://portainer.example.com`, with no
  user name, query or `#` part;
- the access token.

## Add the target

Open **Deployments › Targets → Add a deployment target** and choose
**Portainer environment**.

| Field | What to enter |
| --- | --- |
| name | A short name for the target, such as `home-portainer` |
| URL secret | The name of the secret holding the Portainer address |
| API key secret | The name of the secret holding the access token |
| environment | The environment's name as Portainer shows it |

**Check** finds the environment by its name and reads the Docker daemon's
identity behind it. Nothing is recorded until you press **Add target**. Then
set its offer and facts:

- **Apps domain**: the domain apps are reached under.
- **Disks**: tick it when deployments may keep a data folder here.
- **Needs**: **emulators on the host** runs each declared service as a
  container in the stack.

Once added, the target stays on that environment's Docker daemon. Renaming the
environment in Portainer cannot move it elsewhere. To use another
environment, add another target.

## Check it worked

The target's page shows **Target check:** "Portainer answers" with the
environment's number. Its **Capacity** page is read through Portainer, as for
a Docker host. Other stacks in the environment count as containers AppTown did
not start.

## What AppTown creates and deletes

For each deployment AppTown creates one stack, `apptown-dep-…`, and a volume
for each service that keeps data. Taking a deployment down takes a final
backup through Portainer, then deletes the stack and the volumes its deploys
made. A volume that existed before is copied and kept. AppTown never changes
other stacks, environments or Portainer's settings.

## Costs and limits

You pay for the Docker machine. AppTown shows no cost for it.

- A liveness check, and any readiness check that is not HTTP or is on a
  non-public part, is refused.
- **Stop** stops only the app's containers; its services keep running.
- Backups go through Portainer ([Deployment backups](deployment-backups.md)).
- Container logs and diagnostics are not reported for
  Portainer deployments.

## The intranet target

An AppTown can also have a target named `intranet`, made when AppTown starts
from the **portainer-deploy** connection under **Integrations › Deploy
connectors**. It reads the secrets `PORTAINER_URL`, `PORTAINER_API_KEY` and
`APPTOWN_APPS_DOMAIN`. It confirms its environment when AppTown starts; until
it has, releases to it are refused and its page says the endpoint is not
observed.
