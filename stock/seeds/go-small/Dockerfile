# Packaging recipe for this way of working (built by AppTown at release).
# Adapt the build/run lines to the app; keep the blessed base images.
# Build names ONE main package (`.` here; `./cmd/yourapp` if main lives
# there) — `go build -o file ./...` breaks the moment a second package
# exists, and every real layout has one.
# The runtime image is distroless's nonroot variant: no shell, no package
# manager, and the app runs as an unprivileged user.
# Each RUN that downloads mounts the extra certificates AppTown gives a build
# (the secret apptown-trust) and runs them first; without them it runs as is.
# The runtime has no shell to run them, so the build also writes them to
# /trust, and the runtime takes that file into /etc/ssl/certs, where Go reads
# every file beside the image's own roots. With none given /trust is empty
# and the runtime keeps only its own roots. A secret is not part of the build
# cache's key; the ARG AppTown sets to the certificates' checksum is, so the
# line that writes them runs again when they change.
FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; CGO_ENABLED=0 go build -o /app .
ARG APPTOWN_TRUST_SUM
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; mkdir -p /trust && if [ -n "${APPTOWN_TRUST_CERTS:-}" ]; then install -m 644 "$APPTOWN_TRUST_CERTS" /trust/apptown-trust.pem; fi

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /trust/ /etc/ssl/certs/
COPY --from=build /app /app
USER nonroot:nonroot
EXPOSE 8080
CMD ["/app"]
