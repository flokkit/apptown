# A forge-native contract; Compose is generated for local development

**The application contract is `.forge/app.yaml`, a forge-native document.
Compose is a generated target for local development, never authored.** The
shipped [contract reference](../../stock/docs/guides/contract-reference.md)
describes the format.

`app:` is a map of named parts with exactly one `public: true`. Each part uses
Compose's conventions for what an app's own containers are (`build`, `image`
or `static`, `command`, `ports`, `environment`) plus forge-native fields
(`dataDir`, `seed`, `public`). What the app depends on lives under `needs:` as
first-class entries with typed parameters, with a `properties:` map for rare
provisioning options. `forge config schema` and `forge config validate` let a
model read the schema and check a file.

## Why not a constrained Compose file

A Compose file only naturally describes the app's own containers. Everything
else, its dependencies above all, would be forge intent bolted onto Compose's
`x-` extension slot. And such a file is not really runnable: a need such as
Postgres is fulfilled by the forge, not declared as a service, so
`docker compose up` on it starts the app with no database. The contract is two
things, how the app's own containers run and what the app depends on, and one
syntax serves neither.

## Local development

`forge dev` has two gears:

- `forge dev` brings up the app's needs as local containers and writes `.env`;
  the person or agent runs the app's own processes against them. This is the
  inner loop.
- `forge dev up` renders the whole app, its parts and each need's local
  fulfillment with the connection environment wired in, to
  `.forge/docker-compose.yml`, and runs it. `forge dev down` tears either down.

The Compose file is regenerated on every run, so it cannot drift from the
contract; there is no second copy of the truth to keep in sync, and nothing to
add to the project's `.gitignore`. Previews, deployments and agent steps do not
go through it: the forge fulfils needs directly. Containers are the medium of
the gates; processes are the medium of iteration.

The local machine is one more place the same needs are fulfilled, beside
previews and deployment targets, not a special case.
