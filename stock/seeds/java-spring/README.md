# This project

A Spring Boot web service on Java 21, built with Maven.

## Run

    mvn test
    mvn spring-boot:run
    docker build -t app . && docker run --rm -p 8080:8080 app

The service listens on port 8080 and answers `GET /healthz` with 200 when
it is ready.
