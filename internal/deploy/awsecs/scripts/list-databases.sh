set -eu
apk add --no-cache jq >/dev/null
psql -v ON_ERROR_STOP=1 -At -v instance="$APPTOWN_RDS_INSTANCE" -v provider="$APPTOWN_PROVIDER_OWNED" > /tmp/databases.json <<'SQL'
SELECT json_build_object('instance', :'instance', 'databases', (SELECT json_agg(datname ORDER BY datname) FROM pg_database WHERE NOT datistemplate), 'providerOwned', ARRAY[:'provider'], 'empty', '{}'::json);
SQL
jq -r '.databases[] | @base64' /tmp/databases.json > /tmp/database-names
while IFS= read -r encoded; do
 database=$(printf '%s' "$encoded" | base64 -d; printf .)
 database=${database%.}
 if [ "$database" = "$APPTOWN_PROVIDER_OWNED" ]; then continue; fi
 count=$(PGDATABASE="$database" psql -v ON_ERROR_STOP=1 -Atc "__USER_TABLE_COUNT__")
 case "$count" in ''|*[!0-9]*) exit 1;; esac
 empty=false
 if [ "$count" = 0 ]; then empty=true; fi
 jq --arg db "$database" --argjson empty "$empty" '.empty[$db] = $empty' /tmp/databases.json > /tmp/databases-next.json
 mv /tmp/databases-next.json /tmp/databases.json
done < /tmp/database-names
cat /tmp/databases.json
