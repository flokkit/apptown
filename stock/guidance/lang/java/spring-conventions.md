---
name: lang/java/spring-conventions
tags: lang,java,spring,style
---
Spring the boring way:

- Constructor injection only — no field `@Autowired`. Beans are final
  fields; if a constructor gets crowded, the class is doing too much.
- `@ConfigurationProperties` records over sprinkled `@Value`. Java records
  for DTOs and value types; no Lombok — records and plain code cover it.
- Postgres via Spring Data JPA + Flyway migrations under
  `src/main/resources/db/migration` (never `ddl-auto=update` outside local
  dev). Tests run against the real Postgres service container the workflow
  provides, not H2.
- Controllers stay thin: bind, validate (`jakarta.validation`), delegate to
  a service, return DTO records. Exceptions map in one
  `@RestControllerAdvice`.
- Actuator health at `/actuator/health` is the deploy contract's probe.
- TWO toolchain declarations, keep them consistent. `mise.toml` pins the
  JDK the FORGE uses to build and test your app in its steps (dev/build time).
  The `Dockerfile` is the DEPLOY artifact and carries its own JVM — a build
  stage (`maven:…-temurin-N`, recompiles cold at deploy) and a runtime stage
  (`eclipse-temurin:N-jre`, runs the jar). mise is NOT present at deploy. So the
  Dockerfile's base Java version MUST match `mise.toml`'s `java` — otherwise you
  test on one JVM and ship on another. (Multi-stage: build with the JDK, run on
  the JRE — the runtime is the last FROM.)
