# The desktop trial

`curl -fsSL https://apptown.dev/try | sh` starts AppTown on your own computer
([Running AppTown](/docs/guides/running-apptown)); the script is in the
repository's `site/try.sh`, to read before running. `sh -s -- stop` stops it
with its data kept, and `sh -s -- uninstall` removes the container, its data,
its image and, on macOS, the trust in its certificate.

## The desktop trial's HTTPS

The trial serves itself and every app at `https://…localhost:7777`
addresses: AppTown at `https://localhost:7777`, a change's preview at
`https://change-<id>.preview.localhost:7777`, a deployment under
`main.localhost`. Apps get HTTPS on your computer as they do on a server,
so an app that sends everything to HTTPS works the same in both places.

The certificates come from a certificate authority AppTown makes the first
time it starts and keeps in its volume (`local-tls/ca.pem` in its data
folder; the authority's key never leaves it). The authority can vouch only
for `localhost` names and this computer's own addresses: its certificate
says so in a part every browser enforces, so even a copy of its key could
not be used to pretend to be a real website.

Until your computer trusts the authority, your browser shows a warning for
each address. You can click through each warning and everything works; an
app's request to stay on HTTPS is ignored while the certificate is not
trusted, so nothing locks you out. Trusting it once removes the warnings.

## Trust the trial's certificate

First copy the certificate out of the container:

```sh
docker cp apptown:/var/lib/apptown/forge/local-tls/ca.pem apptown-local-ca.pem
```

**macOS** (Safari, Chrome and Firefox read the login keychain; it asks for
your password):

```sh
security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db apptown-local-ca.pem
```

To remove it: `security delete-certificate -c "AppTown local CA <id>" ~/Library/Keychains/login.keychain-db`,
with the name Keychain Access shows for it.

**Linux.** Command-line tools read the system store; Chrome and Firefox
each read their own. `certutil` is in `libnss3-tools` (Debian, Ubuntu) or
`nss-tools` (Fedora).

```sh
# System store (curl and other tools)
sudo cp apptown-local-ca.pem /usr/local/share/ca-certificates/apptown-local-ca.crt
sudo update-ca-certificates            # Fedora/Arch: sudo trust anchor apptown-local-ca.pem

# Chrome: from Chrome 146 its database is ~/.local/share/pki/nssdb;
# an existing ~/.pki/nssdb is used where it exists
db="$HOME/.pki/nssdb"; [ -d "$db" ] || db="$HOME/.local/share/pki/nssdb"
mkdir -p "$db"; [ -f "$db/cert9.db" ] || certutil -d "sql:$db" -N --empty-password
certutil -d "sql:$db" -A -t C,, -n "AppTown local CA" -i apptown-local-ca.pem

# Firefox: each profile keeps its own (the snap's are under ~/snap/firefox)
for p in ~/.mozilla/firefox/*/cert9.db ~/snap/firefox/common/.mozilla/firefox/*/cert9.db; do
  [ -f "$p" ] || continue
  certutil -d "sql:$(dirname "$p")" -A -t C,, -n "AppTown local CA" -i apptown-local-ca.pem
done
```

Without commands, each browser imports it in its settings: in Chrome,
Privacy and security › Security › Manage certificates › Installed by you ›
Import, as a trusted certificate; in Firefox, Privacy & Security ›
Certificates › View Certificates › Authorities › Import, trusted for
websites. Restart the browser afterwards.

To remove it on Linux, keep `apptown-local-ca.pem` until you are done, then:

```sh
# System store
sudo rm -f /usr/local/share/ca-certificates/apptown-local-ca.crt
sudo update-ca-certificates --fresh    # Fedora/Arch instead: sudo trust anchor --remove apptown-local-ca.pem

# Chrome (its current database, or the older one where that exists) and each Firefox profile
db="$HOME/.pki/nssdb"; [ -d "$db" ] || db="$HOME/.local/share/pki/nssdb"
certutil -d "sql:$db" -D -n "AppTown local CA"
for p in ~/.mozilla/firefox/*/cert9.db ~/snap/firefox/common/.mozilla/firefox/*/cert9.db; do
  [ -f "$p" ] || continue
  certutil -d "sql:$(dirname "$p")" -D -n "AppTown local CA"
done
```

or delete "AppTown local CA" in each browser's certificate settings.

**Windows** is not supported yet.

Removing the trial's volume (`docker volume rm apptown`) removes the
authority; a new trial makes a new one, to be trusted again. Remove the old
one from your computer as above.

Chats on the trial already trust the authority and reach previews at the
same addresses: AppTown gives each chat the certificate (not the key) and
leads the chat's own `localhost:7777` to AppTown, so a chat cannot run its
own server on port 7777.
