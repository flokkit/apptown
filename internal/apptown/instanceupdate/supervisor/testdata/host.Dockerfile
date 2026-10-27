# Disposable qualification host, never a production installation image.
FROM docker:27.5.1-dind@sha256:aa3df78ecf320f5fafdce71c659f1629e96e9de0968305fe1de670e0ca9176ce AS docker
FROM golang:1.26.10-trixie@sha256:9ba7dc6d09ebf6e9317263973174bbf44e15bdf58cb0103747d7376f7cfa2d2f
RUN apt-get update && apt-get install -y --no-install-recommends systemd dbus iptables python3 && rm -rf /var/lib/apt/lists/*
COPY --from=docker /usr/local/bin/docker /usr/local/bin/dockerd /usr/local/bin/containerd /usr/local/bin/containerd-shim-runc-v2 /usr/local/bin/ctr /usr/local/bin/runc /usr/local/bin/docker-init /usr/local/bin/docker-proxy /usr/local/bin/
COPY prepare-host.sh verify-host.sh /usr/local/lib/apptown-u2/
RUN sh /usr/local/lib/apptown-u2/prepare-host.sh
ENV SYSTEMD_UNIT_PATH=/etc/systemd/system \
    SYSTEMD_GENERATOR_PATH=/etc/apptown-u2/no-generators \
    SYSTEMD_ENVIRONMENT_GENERATOR_PATH=/etc/apptown-u2/no-generators
RUN sh /usr/local/lib/apptown-u2/verify-host.sh
STOPSIGNAL SIGRTMIN+3
ENTRYPOINT ["/lib/systemd/systemd"]
