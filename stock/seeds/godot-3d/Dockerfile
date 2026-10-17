# Packaging recipe for this way of working (built by the forge at release).
# Stage 1 exports the game for the web with the pinned Godot; stage 2 serves
# the static result. Neither the engine nor the toolchain ships to players.
FROM barichello/godot-ci:4.4.1 AS export
WORKDIR /game
COPY . .
# Import first (the .godot cache is never committed), then export the Web preset.
RUN mkdir -p /game/build/web \
    && (godot --headless --import >/dev/null 2>&1 || true) \
    && godot --headless --export-release Web /game/build/web/index.html \
    && test -f /game/build/web/index.wasm && test -f /game/build/web/index.pck

# The runtime stage takes Alpine's security updates at build time; Caddy
# runs as an unprivileged user on 8080.
FROM caddy:2-alpine
RUN apk upgrade --no-cache && adduser -D -u 10001 app
COPY --from=export /game/build/web /srv
COPY healthz /srv/healthz
COPY Caddyfile /etc/caddy/Caddyfile
USER app
EXPOSE 8080
CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile"]
