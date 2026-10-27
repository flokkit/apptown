#!/bin/sh
# Non-boot image proof; no systemd manager, Docker or privileged operation.
set -eu
[ "$SYSTEMD_UNIT_PATH" = /etc/systemd/system ]
[ "$SYSTEMD_GENERATOR_PATH" = /etc/apptown-u2/no-generators ]
[ "$SYSTEMD_ENVIRONMENT_GENERATOR_PATH" = /etc/apptown-u2/no-generators ]
[ -z "$(find /etc/apptown-u2/no-generators -mindepth 1 -print -quit)" ]
[ "$(readlink /etc/systemd/system/default.target)" = multi-user.target ]
python3 - <<'PY'
from pathlib import Path
units=Path('/etc/systemd/system')
allowed={'default.target','systemd-journald.service','systemd-journald.socket','systemd-journald-dev-log.socket'}|{n+'.target' for n in ('sysinit','basic','sockets','multi-user','network','network-online','shutdown','final')}
assert {p.name for p in units.iterdir()}==allowed
for p in units.iterdir():
 if p.name=='default.target':
  assert p.is_symlink() and p.resolve()==units/'multi-user.target'
  continue
 assert p.is_file() and not p.is_symlink(),p
 for line in p.read_text().splitlines():
  key,sep,value=line.partition('=')
  if key in {'Requires','Wants','BindsTo','Upholds','Sockets','Service'}:
   assert sep and set(value.split())<=allowed,(p,line)
  if key.startswith('Exec'):
   assert p.name=='systemd-journald.service' and key=='ExecStart' and value in {'/usr/lib/systemd/systemd-journald','/lib/systemd/systemd-journald'},(p,line)
print('Exact fixture unit graph, links and activating dependencies verified.')
PY
/lib/systemd/systemd --version
systemd-analyze unit-paths
