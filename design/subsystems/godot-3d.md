# Godot web projects

The Godot seed and guidance provide a starting point for browser games. They use
the same project, workflow, Docker build and deployment mechanisms as other apps;
there is no Godot-specific execution engine in Forge.

The seed's Dockerfile imports and exports the project with a pinned Godot build
image, checks for expected Web outputs, then serves them with Caddy. The export
preset, server configuration and native app declaration are versioned project
inputs. Editing a game does not require installing Godot into the daemon.

A chat is not guaranteed to have the Godot executable on PATH. Use the project's
build image or declare the matching local development toolchain. Private Docker
availability follows the [runtime guide](../../stock/docs/guides/runtime-environments.md).
A cold preview still builds independently of the session's warm state.

Guidance covers browser export, input and readable scripts. Passing a headless
export establishes build evidence; it does not prove controls, rendering or game
behaviour in a browser. Test those separately.

## Implementation references

- [stock/seeds/godot-3d/Dockerfile](../../stock/seeds/godot-3d/Dockerfile)
- [stock/seeds/godot-3d/export_presets.cfg](../../stock/seeds/godot-3d/export_presets.cfg)
- [stock/seeds/godot-3d/Caddyfile](../../stock/seeds/godot-3d/Caddyfile)
- [stock/guidance/game/godot-web.md](../../stock/guidance/game/godot-web.md)
