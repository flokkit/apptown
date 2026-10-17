# Packaging recipe for this way of working (built by the forge at release).
# Adapt the build/run lines to the app; keep the blessed base images.
# Build names ONE main package (`.` here; `./cmd/yourapp` if main lives
# there) — `go build -o file ./...` breaks the moment a second package
# exists, and every real layout has one.
# The runtime image is distroless's nonroot variant: no shell, no package
# manager, and the app runs as an unprivileged user.
FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /app /app
USER nonroot:nonroot
EXPOSE 8080
CMD ["/app"]
