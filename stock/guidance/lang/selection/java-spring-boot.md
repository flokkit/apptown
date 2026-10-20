---
name: lang/selection/java-spring-boot
tags: lang,java,spring
checks: [{"name": "datasource-from-env", "expects": "the datasource is built from the injected PG* variables at startup, so the same image runs on the built-in host and on a hosted platform", "severity": "medium", "evaluator": "sh", "sh": "[ -f .apptown/app.yaml ] && grep -q 'postgres' .apptown/app.yaml && [ -d src/main ] || exit 0; if grep -rqE 'jdbc:postgresql://[a-zA-Z0-9._-]+:[0-9]+' .apptown/app.yaml src/main/resources 2>/dev/null; then echo 'the datasource URL is a literal host and port (jdbc:postgresql://host:port/db in .apptown/app.yaml or application properties) — on a hosted platform the database arrives as a Unix socket directory in PGHOST, so the app must build its JDBC URL from PGHOST/PGPORT/PGDATABASE/PGUSER/PGPASSWORD at startup (see the block: Database from the environment, on any tier)'; exit 1; fi; if ! grep -rq 'PGHOST' src/main 2>/dev/null; then echo 'nothing in src/main reads PGHOST — build the datasource from the injected PG* variables at startup so the same image runs on the built-in host and on a hosted platform (see the block: Database from the environment, on any tier)'; exit 1; fi", "title": "Configure the database connection through the environment"}, {"name": "gitignore-names-build-output", "expects": "git would ignore what Maven writes (target/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in target/probe.class; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "runtime-stage-not-build-image", "expects": "the Dockerfile's final stage is not FROM maven (any tag), the image this pack's seed builds with: a build stage makes the artifact and the final stage starts from eclipse-temurin:21-jre-noble with only that artifact copied in. Judged from the FROM instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage name resolves to the image it names); a base image given by a build argument is not judged, an instruction with two heredocs, or heredoc-like text inside a quoted shell string whose end is never found, is reported as not judged", "severity": "high", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; img=$(awk 'function inst(i, s, a, d, c) { if (toupper($1) == \"FROM\") { i = 2; while ($i ~ /^--/) i++; s = $i; a = tolower(s); if (a in st) s = st[a]; if (toupper($(i+1)) == \"AS\") st[tolower($(i+2))] = s; last = s } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else print last }' Dockerfile); [ \"$img\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; r=${img%@*}; case ${r##*/} in *:*) r=${r%:*};; esac; r=${r#docker.io/}; r=${r#library/}; [ \"$r\" = maven ] || exit 0; echo \"the final stage runs FROM $img, the image your pack's seed builds with: its compiler, toolchain and their known vulnerabilities ship with the app. Build in a stage FROM maven, then copy only the jar into a final stage FROM eclipse-temurin:21-jre-noble (lang/selection/java-spring-boot shows the recipe)\"; exit 1", "title": "Run the application from a runtime image, not the build image"}]
---
Java services are current stable Spring Boot on Java 21 LTS (Temurin —
pinned in mise.toml; other vendors bypass the artifact mirror). The pack
seeds the packaging recipe, the ignore rules and the runtime contract;
the app comes from Spring Initializr, which is always current, never from
a hand-kept skeleton or a pom written from memory (a remembered Boot
version is a stale one, and the landing's image scan refuses it).

**The first act on an empty repo** (the "build the first version" change):

    curl -fsS https://start.spring.io/starter.tgz \
      -d type=maven-project -d language=java -d javaVersion=21 \
      -d groupId=app -d artifactId=app -d name=app -d packageName=app \
      -d dependencies=web,data-jpa,postgresql,flyway,validation,actuator \
      | tar -xzf - --skip-old-files --exclude=mvnw --exclude=mvnw.cmd --exclude=.mvn

Wrapper scripts are excluded on purpose (the rule below); `--skip-old-files`
keeps the seed's `.gitignore` and README. In the same change, add the
managed-version overrides below to the generated `pom.xml`: without them
the first landing's image scan fails on the Tomcat and Jackson that
Initializr's Boot manages. The seeded Dockerfile builds
with plain `mvn` and runs the jar as an unprivileged user. Add a
`/healthz` endpoint (one `@GetMapping` returning 200, no database) — the
seeded contract at `.apptown/app.yaml` names it as the readiness path and
already declares the Postgres need; Flyway migrates at boot, so no init
step is needed:

    app:
      web:
        build: .
        ports: ["8080"]
        public: true
        health:
          readiness: { http: /healthz }
    needs:
      postgres:
        version: "16"

**Managed versions that pass the image scan.** The landing scans the
built image and refuses any fixed vulnerability, and a Boot release often
manages a Tomcat or Jackson that has had a fix since. Raise the managed
version with Boot's own property in `pom.xml` rather than pinning
dependencies one by one. On Boot 4.1.1 (Initializr's default in October
2026) these two were needed: verified on 2026-10-03 by building the
seed's image from a fresh Initializr project and scanning it with the
landing's trivy flags (without them: 3 critical Tomcat and 7 Jackson
findings; with them: none):

    <properties>
      <java.version>21</java.version>
      <tomcat.version>11.0.26</tomcat.version>
      <jackson-bom.version>3.1.7</jackson-bom.version>
    </properties>

Before handing back, confirm what will ship:
`mvn dependency:tree -Dincludes=org.apache.tomcat.embed,tools.jackson.core`
must show versions at least those above. When Initializr's Boot is newer
than 4.1.1, compare its managed versions with these and keep an override
only while it raises one. When the scan names another managed library, do the same with its
property (`<postgresql.version>`, `<log4j2.version>`, …) set to the fixed
release the finding names, and drop the override once Boot manages that
version or newer.

A finding that cannot be fixed this way is recorded as an exception by a
person; build/container-image says how.

Read the database from the injected `PG*` variables (the datasource bean
under "Database from the environment" below; never parse `DATABASE_URL`),
add one controller and one test that hits it (`mvn test`).

- **Build with the JDK, run on the JRE.** Maven builds in
  `maven:3.9-eclipse-temurin-21`; the final stage is
  `eclipse-temurin:21-jre-noble` with only the jar copied in (the seed). Neither
  the Maven image nor a JDK ships: a jar needs a JRE, and the compiler,
  Maven and its local repository are size and scan findings with no use
  at run time. The tag names the Ubuntu release on purpose: the plain
  `21-jre` tag is Ubuntu 26.04, whose base image carries `/usr/bin/pebble`,
  a Go binary no package owns, so `apt-get upgrade` cannot clear the image
  scan's findings in it. A project on `eclipse-temurin:21-jre` whose scan
  names `usr/bin/pebble` changes its final `FROM` to this tag.
- **Maven, not Gradle — a deliberate choice, not inertia.** Three reasons,
  in order: (1) `pom.xml` is DATA; a Gradle build is a PROGRAM. Builds here
  are largely agent-authored and cop-reviewed — a declarative POM is
  auditable at a glance, while build scripts accumulate cleverness exactly
  where it is least wanted. (2) The mirror path is proven end-to-end for
  Maven (tool via apache-dist, dependencies via the ambient settings.xml,
  verified with Central unreachable); Gradle would additionally need
  plugins.gradle.org proxied and an init-script ambient layer that does not
  exist yet. (3) Maven's classic costs — verbosity, slower iteration — fall
  on the agent, not you; small-service cold container builds get little
  from Gradle's daemon and incremental machinery.
- **When to deviate**: a Kotlin codebase, an existing Gradle build being
  adopted, or a large multi-module project where build time demonstrably
  hurts. Deviating is fine — record it in an ADR referencing this guidance,
  and expect Gradle's plugins to come straight from plugins.gradle.org, not
  through AppTown's package mirrors: the plugin proxy and init script that
  would route them there do not exist yet.
- Wrapper scripts (mvnw/gradlew) are unnecessary — the toolchain is
  declared in mise.toml, not vendored into the repo.
- Start from spring-boot-starter-parent; add starters deliberately
  (web, data-jpa, actuator, validation). Every additional starter is a
  dependency decision, not a default.
- One deployable per repo: an executable jar. The service starts with
  `java -jar` and reads ALL environment-specific config from env vars
  (Spring's relaxed binding: `SERVER_PORT`, `SPRING_DATASOURCE_URL`).
- **Trust the front door.** AppTown terminates TLS and forwards the
  scheme and host (`X-Forwarded-Proto: https`, `X-Forwarded-Host`). Set

      server.forward-headers-strategy=framework

  in `application.properties`, so Spring builds redirects and absolute
  links from them. Without it the redirect after a form post
  (`redirect:/items`) sends the browser to `http://`, and only the front
  door's own bounce back to HTTPS hides it.

**Database from the environment, on any tier.**
AppTown delivers a postgres need as environment variables, on every tier:
`PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD` (and a libpq-form
`DATABASE_URL`). On the built-in host `PGHOST` is a hostname. A Google
Cloud SQL database reached from Cloud Run without a VPC arrives instead
as the Unix socket directory the platform mounts,
`/cloudsql/<project>:<region>:<instance>`, and the standard JDBC driver
speaks only TCP:

- **Build the datasource from `PGHOST` at startup, never from a literal.**
  No `jdbc:postgresql://host:port/db` in `.apptown/app.yaml` or the
  properties files. One small configuration class does it:

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

  Tests keep their own datasource (Testcontainers or an H2 profile); this
  bean is for the running app. `SPRING_DATASOURCE_*` variables set by hand
  in the contract are the literal this rule forbids.
- **Only when the app is deployed to a Cloud SQL socket** (a target that
  hands it a `/cloudsql/...` `PGHOST`): add
  `com.google.cloud.sql:postgres-socket-factory` (runtime scope) beside
  `org.postgresql:postgresql` in `pom.xml`; the `/cloudsql/` branch above
  names it. Without such a target, leave it out: it is a dependency the
  app never uses.

Rationale: every other stack we run reaches a socket-mounted database
with its usual driver; Java is the one that needs a library and a URL
shape for it, so the bean handles both shapes and the library joins only
when a deployment needs it.

Build output stays out of git: `.gitignore` names `target/`, which
is everything Maven writes, so a test run can never be committed by
accident.

**Fits a container limit.** The JVM sizes its heap from the memory it can
see: with no limit that is the whole host's, so a small app grows to hundreds
of megabytes; inside a limit it takes a quarter by default. A small Spring
Boot service runs comfortably under about 192 MB: declare
`resources: { memory: 256M }` in `.apptown/app.yaml`, start the JVM with
`-XX:MaxRAMPercentage=75 -XX:+UseSerialGC` (the serial collector suits one or
two CPUs and uses the least memory of its own), and set
`spring.threads.virtual.enabled=true`; the seeded Dockerfile does all three.
The JDK's AOT cache (`-XX:AOTCache`, JDK 25) and a GraalVM native image are
opt-in, not defaults. deploy/memory-declared says how to choose and check it.
