package console

import "strconv"

// LocalTLS is an AppTown serving itself over HTTPS from its own local
// certificate authority (apptownd -local-tls, the desktop trial). A chat on
// that AppTown trusts the authority and reaches AppTown's HTTPS addresses
// (its change's preview) at their public URLs.
type LocalTLS struct {
	CACert []byte // the authority's certificate (PEM), never its key
	Port   int    // the public port, the one in every https://….localhost:<port> address
	Target string // where that port really is from the container: host.docker.internal:<apptownd's port>
	// PreviewDomain is the preview tier's domain (preview.localhost). Tools
	// that ask the system resolver (Python, Node), unlike curl and
	// browsers, do not answer *.localhost names themselves, so a change
	// chat's own preview name is written into its hosts file.
	PreviewDomain string
}

// LocalCAFile is the authority's certificate in a chat's /console folder.
const LocalCAFile = "local-ca.pem"

// LocalTLSPrelude runs in a chat's container before the agent starts, when
// AppTown has a local authority. Every client resolves *.localhost to the
// container's own loopback itself, before any hosts file, so the prelude
// leads that loopback's public port to AppTown: the TLS stream passes
// through untouched and the preview's name arrives as SNI and Host, as from
// a browser. It trusts the authority for curl, Python, Go, Node and
// headless Chromium (both NSS database paths). Everything here fails soft:
// a chat starts even if one tool is missing.
func LocalTLSPrelude(t LocalTLS) string { return localTLSPrelude(Mount+"/"+LocalCAFile, "/tmp", t) }

// localTLSPrelude finds the certificate at ca and writes its bundle under
// scratch.
func localTLSPrelude(ca, scratch string, t LocalTLS) string {
	bundle := scratch + "/forge-ca-bundle.pem"
	port := strconv.Itoa(t.Port)
	return `if [ -f ` + ca + ` ]; then ` +
		`cat /etc/ssl/certs/ca-certificates.crt ` + ca + ` >` + bundle + ` 2>/dev/null; ` +
		`export SSL_CERT_FILE=` + bundle + ` CURL_CA_BUNDLE=` + bundle + ` REQUESTS_CA_BUNDLE=` + bundle + ` NODE_EXTRA_CA_CERTS=` + ca + `; ` +
		`for db in "$HOME/.pki/nssdb" "$HOME/.local/share/pki/nssdb"; do mkdir -p "$db"; ` +
		`[ -f "$db/cert9.db" ] || certutil -d "sql:$db" -N --empty-password >/dev/null 2>&1; ` +
		`certutil -d "sql:$db" -A -t C,, -n "AppTown local CA" -i ` + ca + ` >/dev/null 2>&1; done; ` +
		hostsEntry(t.PreviewDomain) +
		`(socat TCP4-LISTEN:` + port + `,bind=127.0.0.1,fork,reuseaddr TCP:` + t.Target + ` >/dev/null 2>&1 &); ` +
		`(socat TCP6-LISTEN:` + port + `,bind=[::1],fork,reuseaddr TCP:` + t.Target + ` >/dev/null 2>&1 &); ` +
		`fi; true`
}

// hostsEntry names a change chat's own preview (APPTOWN_CHANGE) as this
// container's loopback in /etc/hosts, where the forwarder answers it.
func hostsEntry(previewDomain string) string {
	if previewDomain == "" {
		return ""
	}
	h := `change-$APPTOWN_CHANGE.` + previewDomain
	return `[ -n "$APPTOWN_CHANGE" ] && ! grep -q "` + h + `" /etc/hosts 2>/dev/null && ` +
		`printf '127.0.0.1 %s\n::1 %s\n' "` + h + `" "` + h + `" >>/etc/hosts 2>/dev/null; `
}
