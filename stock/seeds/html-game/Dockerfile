# Packaging recipe for this way of working (built by AppTown at release).
# Base image is part of the way of working — keep the caddy family.
# no-cache (with ETags): after a release, players must see the new game on
# the next load — a silent server invites browsers' heuristic caching.
# Caddy runs as an unprivileged user on 8080.
FROM caddy:2-alpine
RUN adduser -D -u 10001 app
COPY . /srv
RUN printf ':8080 {\n\troot * /srv\n\theader Cache-Control no-cache\n\tfile_server\n}\n' > /etc/caddy/Caddyfile
USER app
EXPOSE 8080
CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile"]
