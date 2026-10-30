# This project

A Rust web service.

## Run

    make test
    cargo run
    docker build -t app . && docker run --rm -p 8080:8080 app

`make test` runs the test suite (`cargo test --locked`). The service
listens on port 8080 (or `$PORT` when set) and answers `GET /healthz`
with 200 when it is ready. `Cargo.lock` is committed; the image build
uses it.
