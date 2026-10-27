#!/bin/sh
# Build-time only, inside the disposable image. PID1 sees only this unit graph.
set -eu
units=/etc/systemd/system
rm -rf "$units"
mkdir -p "$units" /etc/apptown-u2/no-generators /etc/systemd/journald.conf.d
for unit in systemd-journald.service systemd-journald.socket systemd-journald-dev-log.socket; do
 cp "/usr/lib/systemd/system/$unit" "$units/$unit"
done
for target in sysinit basic sockets multi-user network network-online shutdown final; do
 printf '[Unit]\nDescription=Disposable fixture %s\nDefaultDependencies=no\n' "$target" > "$units/$target.target"
done
printf 'Requires=basic.target systemd-journald.socket systemd-journald-dev-log.socket\nAfter=basic.target\n' >> "$units/multi-user.target"
ln -s multi-user.target "$units/default.target"
# Journal only the fixture's own services; no kernel audit configuration/reading.
printf '[Journal]\nAudit=no\nReadKMsg=no\nForwardToKMsg=no\n' > /etc/systemd/journald.conf.d/fixture.conf
