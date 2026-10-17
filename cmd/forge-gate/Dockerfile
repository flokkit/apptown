# forge-gate: the access gate in front of a restored copy of production data.
# Build from the repository root (the module root):
#   make gate-image            (or: docker build --platform linux/amd64 -f cmd/forge-gate/Dockerfile .)
# The gate imports only the standard library, so the build needs go.mod and
# the two gate packages, nothing else; Dockerfile.dockerignore keeps the rest
# of the repository out of the build context.
FROM golang:1.26.9 AS build
WORKDIR /src
COPY go.mod ./
COPY internal/gate/ internal/gate/
COPY cmd/forge-gate/ cmd/forge-gate/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags='-s -w' -o /out/forge-gate ./cmd/forge-gate

# Nothing but the binary and CA certificates: the gate calls the forge over
# https to redeem tickets, and scratch has no certificate store of its own.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/forge-gate /forge-gate
USER 65534
EXPOSE 8080
ENTRYPOINT ["/forge-gate"]
