# This project

A TypeScript web application on Node.

## Run

    npm ci
    npm test
    npm run build && node --run start
    docker build -t app . && docker run --rm -p 8080:8080 app

The server listens on port 8080 and answers `GET /healthz` with 200 when
it is ready.
