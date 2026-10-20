# Packaging recipe for this way of working (built by AppTown at release).
# Generate the app from Spring Initializr (see the java-spring selection
# guidance). Maven comes from the build image — this pack declares its
# toolchain in mise.toml and vendors NO wrapper scripts, so the recipe must
# not expect ./mvnw or ./gradlew in the tree.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
COPY . .
RUN mvn -q package -DskipTests

# The runtime stage takes the distribution's security updates at build
# time, so a known fixed CVE in the base (libssl) does not fail the image
# scan, and runs the jar as an unprivileged user. It names its Ubuntu
# release (noble, 24.04 LTS): the plain 21-jre tag moved to Ubuntu 26.04,
# whose base image carries /usr/bin/pebble, a Go binary no package owns, so
# apt-get upgrade cannot fix the Go CVEs the image scan finds in it.
FROM eclipse-temurin:21-jre-noble
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --no-create-home app
COPY --from=build /src/target/*.jar /app.jar
USER app
EXPOSE 8080
# Memory. Measured on two small Spring Boot + Hibernate + Flyway apps at a
# 256Mi limit through a burst of requests: with these settings they held
# 210-225 MiB, about 150 MiB of it outside the heap (class metadata, code,
# thread stacks, native allocations). With -XX:MaxRAMPercentage=75 the heap
# could grow into that room and one app was killed under load. The
# .apptown/app.yaml declares 320Mi for headroom over those measurements.
#   -XX:MaxRAMPercentage=30     asks for a smaller heap; the JVM's own floor
#                               still allows about 126 MiB at 256-320Mi
#   MALLOC_ARENA_MAX=2          caps malloc arenas at two, reducing native
#                               memory overhead
#   -XX:+UseSerialGC            no collector threads, least memory of its own
#   -XX:TieredStopAtLevel=1     the quick compiler only: less code cache and
#                               compiler memory, slower peak speed a small
#                               service does not need
#   -XX:ReservedCodeCacheSize=32m, -Xss512k   smaller code cache and stacks
# Virtual threads keep request threads cheap (Spring Boot reads this
# variable as spring.threads.virtual.enabled).
ENV MALLOC_ARENA_MAX=2
ENV SPRING_THREADS_VIRTUAL_ENABLED=true
CMD ["java", "-XX:MaxRAMPercentage=30", "-XX:+UseSerialGC", "-XX:TieredStopAtLevel=1", "-XX:ReservedCodeCacheSize=32m", "-Xss512k", "-jar", "/app.jar"]
