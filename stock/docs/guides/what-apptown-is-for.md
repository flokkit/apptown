# What AppTown is for

AppTown is a software factory for the rest of us. It helps a small team keep
the apps it makes with AI coding agents, on a machine or server the team runs
itself.

Coding agents have made a small custom app cheap to write. Owning one that
people rely on is still work: someone has to deploy it, back it up, keep it
patched and still understand it a year later, usually after the person who
made it has moved on. AppTown makes that handover routine.

## What it does

A person, or a coding agent on their behalf, asks for a **change** to an app.
AppTown works the change through the project's **way of working**, which
gives every app the habits of a careful engineering team:

- agents do the work in containers, on AppTown's host or a worker;
- AppTown runs the tests and checks itself, instead of taking the agent's
  word for it;
- the change boots on a **preview** from its declared contract, so you can
  try it before it goes live;
- a person approves it where the way of working says a person must;
- AppTown merges it into the app's `main` branch and deploys it.

Every change keeps its record: who asked, which rules applied and at which
version, what each check found, who approved and what their approval meant,
and what is running where.

A **way of working** is data: the guidance agents follow, workflows, checks,
policies and approvals, versioned and adopted per project. A team can have
several, one for its Python web apps and one for its static sites, say.
Changing how a team works is a change to that data, not to AppTown.

Apps describe how they run in one small file, `.apptown/app.yaml`
([the app contract](contract-reference.md)). AppTown runs them on a
Docker host, in a Portainer environment, on DigitalOcean App Platform or as a
bucket website, and gives each app the databases and storage it declares,
backed up ([Previews and deployments](container-lifecycle.md) says
what each target gets).

## Who it is for

Small teams running their own internal software: tools, forms, dashboards,
small APIs and websites, mostly web applications. One or more admins run
AppTown for everyone else; each project's own people approve its changes.

It is not built for mobile apps, for large systems with many services and
teams, or for hosting software for paying customers at scale. It is not
low-code, and it does not replace your Git host or ticket tracker. What it
produces is ordinary software in an ordinary Git repository and container
image, which you can take elsewhere.

## Read next

- [Start here](quickstart.md): make a first change.
- [How AppTown works](architecture.md): the pieces and how a change
  moves.
- [What 1.0 promises](compatibility.md).
