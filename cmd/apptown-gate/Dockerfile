# apptown-gate: the access gate in front of a restored copy of production data.
# Build from the repository root (the module root):
#   make gate-image            (or: docker build --platform linux/amd64 -f cmd/apptown-gate/Dockerfile .)
# The gate imports the standard library and internal/clock (itself standard
# library only), so the build needs go.mod and those three packages, nothing
# else; Dockerfile.dockerignore keeps the rest of the repository out of the
# build context. A new import of the module's own packages goes in both.
FROM golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS build
WORKDIR /src
COPY go.mod ./
COPY internal/clock/clock.go internal/clock/
COPY internal/gate/ internal/gate/
COPY cmd/apptown-gate/ cmd/apptown-gate/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags='-s -w' -o /out/apptown-gate ./cmd/apptown-gate

# Nothing but the binary and CA certificates: the gate calls AppTown over
# https to redeem tickets, and scratch has no certificate store of its own.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/apptown-gate /apptown-gate
USER 65534
EXPOSE 8080
ENTRYPOINT ["/apptown-gate"]
