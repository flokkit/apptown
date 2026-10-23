# Packaging recipe for this way of working (built by AppTown at release).
# Adapt the build/run lines to the app; keep the blessed base images.
# Build names ONE main package (`.` here; `./cmd/yourapp` if main lives
# there) — `go build -o file ./...` breaks the moment a second package
# exists, and every real layout has one.
# The runtime image is distroless's nonroot variant: no shell, no package
# manager, and the app runs as an unprivileged user.
# Each RUN that downloads mounts the extra certificates AppTown gives a build
# (the secret apptown-trust) and runs them first; without them it runs as is.
FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; CGO_ENABLED=0 go build -o /app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /app /app
USER nonroot:nonroot
EXPOSE 8080
CMD ["/app"]
