# This project

A Rails web application on Ruby 3.4.

## Run

    bundle install
    make test
    bin/rails server
    docker build -t app . && docker run --rm -p 8080:8080 app

`make test` prepares the test database and runs the test suite
(`bin/rails test`). The application listens on port 8080 and answers
`GET /up` with 200 when it is ready.
