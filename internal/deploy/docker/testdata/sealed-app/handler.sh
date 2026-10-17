#!/bin/sh
read -r method path proto
host=""
while read -r line; do
  line=$(printf '%s' "$line" | tr -d '\r')
  [ -z "$line" ] && break
  case "$line" in [Hh]ost:*) host=$(printf '%s' "${line#*:}" | tr -d ' ');; esac
done
allowed=no
for o in $(printf '%s' "$FORGE_ORIGINS" | tr ',' ' '); do
  [ "${o#https://}" = "$host" ] && allowed=yes
done
answer() { printf 'HTTP/1.1 %s\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n%s\n' "$1" "$2"; }
case "$path" in
  /healthz) answer "200 OK" "up" ;;
  *) if [ "$allowed" != yes ]; then answer "403 Forbidden" "blocked host $host"; exit 0; fi
     case "$path" in
       /data) n=$(psql -tA -c "select count(*) from orders" 2>/dev/null)
              if [ "${n:-0}" -gt 0 ]; then answer "200 OK" "$(psql -tA -c "select email from orders limit 1")"; else answer "500 Internal Server Error" "no orders"; fi ;;
       *) answer "200 OK" "home" ;;
     esac ;;
esac
