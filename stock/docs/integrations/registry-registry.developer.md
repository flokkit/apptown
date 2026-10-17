# Package registries & mirrors — developer guide

## What it's for

Builds pull thousands of packages, and the internet
is slow, rate-limited and occasionally down. Every package your build
asks for comes from a local mirror instead: the same package, fetched
once for the whole instance, then served from next door. Builds get
faster and stop failing for reasons that have nothing to do with your
code.

When the instance has bound a mirror for an ecosystem, your builds are
routed through it. There is nothing to configure and nothing to add to
your repo.

This page is about package mirrors. The container registry that holds a
project's release images is separate; see
[Previews and deployments](/docs/guides/container-lifecycle).

## What you get

- `go`, `npm`, `uv`/`pip`, `cargo`, `mvn` all resolve through local
  mirrors automatically — faster, cached, immune to upstream outages
  and yanked packages.
- Run receipts record which mirrors fed each step.
- Toolchains declared in `mise.toml` install through the same mirrors.

## Rules that matter to you

- Never write a registry/mirror URL into a committed file. The repo
  declares WHAT it depends on; the instance decides WHERE it comes
  from. (Moving instances/orgs then costs zero repo changes.)
- Dockerfiles and CI configs you write do NOT inherit the ambient
  wiring — propagate it explicitly (`ARG NPM_CONFIG_REGISTRY` etc.).
  In an implementation step your private docker daemon does pull
  Docker Hub images through the mirror by itself; only the build's
  RUN steps need the env passed in.
- A slow or failing pull from a public registry usually means that
  ecosystem's mirror isn't bound. Ask the instance admin to bind the
  format so it resolves through the local mirror.

## Seeing what's active

`env | grep -E 'GOPROXY|REGISTRY|INDEX|RUSTUP|MAVEN'` inside any step
or chat, or Ways of working › Guidance, in the Generated section
(`ambient/registries`).
