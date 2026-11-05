---
name: lang/java/spring-conventions
tags: lang,java,spring,style
---
Spring the boring way:

- Constructor injection only, with final bean fields; no field `@Autowired`.
  A crowded constructor means the class does too much.
- `@ConfigurationProperties` records over scattered `@Value`; Java records for
  DTOs and value types. No Lombok: use records and plain code.
- Spring Data JPA and Flyway in `src/main/resources/db/migration` for Postgres.
  Never `ddl-auto=update` outside local dev. Test on the workflow's real Postgres
  service container, never H2.
- Thin controllers bind, validate with `jakarta.validation`, delegate to services
  and return DTO records. Map exceptions in one `@RestControllerAdvice`.
- Actuator health at `/actuator/health` is the deploy contract's probe.
- Keep both Java toolchains consistent: `mise.toml` selects the JDK for AppTown's
  build/tests; the Dockerfile recompiles cold in a `maven:…-temurin-N` JDK stage
  and runs the jar in a final `eclipse-temurin:N-jre` stage. Match both to mise's
  `java` version. mise is absent at deploy.
