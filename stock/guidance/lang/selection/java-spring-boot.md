---
name: lang/selection/java-spring-boot
tags: lang,java,spring
checks: [{"name": "datasource-from-env", "expects": "the datasource is built from the injected PG* variables at startup, so the same image runs on the built-in host and on a hosted platform", "severity": "medium", "evaluator": "sh", "sh": "[ -f .apptown/app.yaml ] && grep -q 'postgres' .apptown/app.yaml && [ -d src/main ] || exit 0; if grep -rqE 'jdbc:postgresql://[a-zA-Z0-9._-]+:[0-9]+' .apptown/app.yaml src/main/resources 2>/dev/null; then echo 'the datasource URL is a literal host and port (jdbc:postgresql://host:port/db in .apptown/app.yaml or application properties) — on a hosted platform the database arrives as a Unix socket directory in PGHOST, so the app must build its JDBC URL from PGHOST/PGPORT/PGDATABASE/PGUSER/PGPASSWORD at startup (see the block: Database from the environment, on any tier)'; exit 1; fi; if ! grep -rq 'PGHOST' src/main 2>/dev/null; then echo 'nothing in src/main reads PGHOST — build the datasource from the injected PG* variables at startup so the same image runs on the built-in host and on a hosted platform (see the block: Database from the environment, on any tier)'; exit 1; fi", "title": "Configure the database connection through the environment"}, {"name": "gitignore-names-build-output", "expects": "git would ignore what Maven writes (target/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in target/probe.class; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "runtime-stage-not-build-image", "expects": "the Dockerfile's final stage is not FROM maven (any tag), the image this pack's seed builds with: a build stage makes the artifact and the final stage starts from eclipse-temurin:21-jre-noble with only that artifact copied in. Judged from the FROM instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage name resolves to the image it names); a base image given by a build argument is not judged, an instruction with two heredocs, or heredoc-like text inside a quoted shell string whose end is never found, is reported as not judged", "severity": "high", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; img=$(awk 'function inst(i, s, a, d, c) { if (toupper($1) == \"FROM\") { i = 2; while ($i ~ /^--/) i++; s = $i; a = tolower(s); if (a in st) s = st[a]; if (toupper($(i+1)) == \"AS\") st[tolower($(i+2))] = s; last = s } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else print last }' Dockerfile); [ \"$img\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; r=${img%@*}; case ${r##*/} in *:*) r=${r%:*};; esac; r=${r#docker.io/}; r=${r#library/}; [ \"$r\" = maven ] || exit 0; echo \"the final stage runs FROM $img, the image your pack's seed builds with: its compiler, toolchain and their known vulnerabilities ship with the app. Build in a stage FROM maven, then copy only the jar into a final stage FROM eclipse-temurin:21-jre-noble (lang/selection/java-spring-boot shows the recipe)\"; exit 1", "title": "Run the application from a runtime image, not the build image"}]
---
Java services are current stable Spring Boot on Java 21 LTS (Temurin, pinned in
mise.toml; other vendors bypass the artifact mirror), built with Maven. The pack
seeds the Dockerfile, ignore rules and contract; the app comes from Spring
Initializr, never from a pom written from memory (a remembered Boot version is
stale and fails the image scan).

**The first act on an empty repo:**

    curl -fsS https://start.spring.io/starter.tgz \
      -d type=maven-project -d language=java -d javaVersion=21 \
      -d groupId=app -d artifactId=app -d name=app -d packageName=app \
      -d dependencies=web,data-jpa,postgresql,flyway,validation,actuator \
      | tar -xzf - --skip-old-files --exclude=mvnw --exclude=mvnw.cmd --exclude=.mvn

No wrapper scripts (mise declares the toolchain); `--skip-old-files` keeps the
seed's files. Add a `/healthz` endpoint (one `@GetMapping`, 200, no database),
one controller and one test that hits it (`mvn test`). The seeded
`.apptown/app.yaml` names `/healthz` and declares the Postgres need; Flyway
migrates at boot, so no init step.

**Managed versions that pass the image scan.** In the same first change raise
Boot's managed Tomcat and Jackson with Boot's own properties in `pom.xml`
(needed on Boot 4.1.1):

    <properties>
      <java.version>21</java.version>
      <tomcat.version>11.0.26</tomcat.version>
      <jackson-bom.version>3.1.7</jackson-bom.version>
    </properties>

Before handing back,
`mvn dependency:tree -Dincludes=org.apache.tomcat.embed,tools.jackson.core`
shows at least those versions. When Initializr's Boot is newer, keep an
override only while it raises a version. When the scan names another managed
library, set its property (`<postgresql.version>`, `<log4j2.version>`, …) to
the fixed release, and drop it once Boot manages that version. A finding that
cannot be fixed this way is recorded by a person (build/container-image).

- **Build with the JDK, run on the JRE.** Build in
  `maven:3.9-eclipse-temurin-21`; the final stage is
  `eclipse-temurin:21-jre-noble` with only the jar (the seed). Not the plain
  `21-jre` tag: it is Ubuntu 26.04, whose `/usr/bin/pebble` fails the scan.
- **Maven, not Gradle.** Gradle only for a Kotlin codebase, an adopted Gradle
  build, or a large multi-module build where build time hurts; record it in an
  ADR, and expect its plugins from plugins.gradle.org (not mirrored).
- Start from spring-boot-starter-parent; each extra starter is a decision.
- One executable jar per repo, started with `java -jar`, all config from env
  vars (`SERVER_PORT`, …).
- **Trust the front door:** `server.forward-headers-strategy=framework` in
  `application.properties`, or redirects after a form post go to `http://`.
- **Error reporting** (Sentry's Spring Boot starter, DSN from `SENTRY_DSN`):
  set `sentry.exception-resolver-order=-2147483648` in
  `application.properties`, or exceptions Spring's own resolvers handle
  first are never reported.

**Database from the environment.** A postgres need arrives as `PGHOST`,
`PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`. Build the datasource from them
at startup; never a literal `jdbc:` URL in the contract or properties, never
`SPRING_DATASOURCE_*` set by hand, never parse `DATABASE_URL`. A Cloud SQL
socket arrives as a `PGHOST` of `/cloudsql/<project>:<region>:<instance>`:

    @Configuration
    class DataSourceFromEnv {
      @Bean DataSource dataSource(Environment env) {
        String host = env.getRequiredProperty("PGHOST");
        String db = env.getRequiredProperty("PGDATABASE");
        String url = host.startsWith("/cloudsql/")
            ? "jdbc:postgresql:///" + db + "?cloudSqlInstance=" + host.substring("/cloudsql/".length())
              + "&socketFactory=com.google.cloud.sql.postgres.SocketFactory"
            : "jdbc:postgresql://" + host + ":" + env.getProperty("PGPORT", "5432") + "/" + db;
        return DataSourceBuilder.create().url(url)
            .username(env.getRequiredProperty("PGUSER"))
            .password(env.getRequiredProperty("PGPASSWORD")).build();
      }
    }

Tests keep their own datasource (Testcontainers or an H2 profile). Add
`com.google.cloud.sql:postgres-socket-factory` (runtime scope) only when a
target hands the app a `/cloudsql/` host.

`.gitignore` names `target/`.

**Memory:** 320Mi with the JVM settings in deploy/memory-declared (the seeded
Dockerfile applies them). The JDK's AOT cache and GraalVM native images are
opt-in, never defaults.
