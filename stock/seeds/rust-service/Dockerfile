# Packaging recipe for this way of working (built by the forge at release).
# Adapt the binary name; keep the rust/debian base family. The rust minor
# matches the mise.toml pin — dev and release build with the same compiler.
# cargo install stages ONLY the built binaries under /out/bin (a raw
# target/release/* copy drags deps/, build/, and .d files along and the
# container execs the wrong one). --locked builds what Cargo.lock says, so
# commit Cargo.lock.
FROM rust:1.84 AS build
WORKDIR /src
COPY . .
RUN cargo install --locked --path . --root /out

# The runtime stage takes the distribution's security updates at build
# time, so a known fixed CVE in the base does not fail the image scan.
FROM debian:bookworm-slim
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --no-create-home app
COPY --from=build /out/bin/ /usr/local/bin/
USER app
EXPOSE 8080
CMD ["sh", "-c", "exec $(ls /usr/local/bin | head -1)"]
