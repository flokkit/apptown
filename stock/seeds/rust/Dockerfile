# Packaging recipe for this way of working (built by AppTown at release).
# Adapt the binary name; keep the rust/debian base family. The rust minor
# matches the mise.toml pin — dev and release build with the same compiler.
# cargo install stages ONLY the built binaries under /out/bin (a raw
# target/release/* copy drags deps/, build/, and .d files along and the
# container execs the wrong one). --locked builds what Cargo.lock says, so
# commit Cargo.lock.
# Each RUN that downloads mounts the extra certificates AppTown gives a build
# (the secret apptown-trust) and runs them first; without them it runs as is.
FROM rust:1.84 AS build
WORKDIR /src
COPY . .
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; cargo install --locked --path . --root /out

# The runtime stage takes the distribution's security updates at build
# time, so a known fixed CVE in the base does not fail the image scan.
FROM debian:bookworm-slim
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --no-create-home app
COPY --from=build /out/bin/ /usr/local/bin/
USER app
EXPOSE 8080
# The command first trusts the extra certificates AppTown gives the
# running app (APPTOWN_TRUST; nothing without it), then runs the app.
CMD ["sh", "-c", "eval \"${APPTOWN_TRUST:-}\"; exec $(ls /usr/local/bin | head -1)"]
