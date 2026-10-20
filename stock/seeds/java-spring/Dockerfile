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
# The JVM sizes its heap from the memory it can see: the part's declared
# limit (256M in .forge/app.yaml), of which it takes a quarter by default.
# 75% gives the heap room and leaves the rest for the JVM's own (metaspace,
# threads, code cache). The serial collector suits a small service on one
# or two CPUs: no collector threads and the least memory of its own. Virtual
# threads keep request threads cheap (Spring Boot reads this variable as
# spring.threads.virtual.enabled).
ENV SPRING_THREADS_VIRTUAL_ENABLED=true
CMD ["java", "-XX:MaxRAMPercentage=75", "-XX:+UseSerialGC", "-jar", "/app.jar"]
