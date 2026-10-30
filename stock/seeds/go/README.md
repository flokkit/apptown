# This project

A Go web service.

## Run

    go test ./...
    go run .
    docker build -t app . && docker run --rm -p 8080:8080 app

The service listens on port 8080 and answers `GET /healthz` with 200 when
it is ready.
