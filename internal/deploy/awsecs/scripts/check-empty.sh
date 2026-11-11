set -eu
count=$(psql -v ON_ERROR_STOP=1 -Atc "__USER_TABLE_COUNT__")
[ "$count" = 0 ]
printf 'empty\n'
