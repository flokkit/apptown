# AppTown's certificate installer (internal/trust/install.sh). AppTown
# puts the extra certificate authorities in it and hands it to a container
# as APPTOWN_TRUST, or to a build as the secret apptown-trust. Running it,
#   eval "${APPTOWN_TRUST:-}"
# or in a Dockerfile RUN line
#   eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"
# adds the authorities to what this shell's programs trust, beside the
# image's own roots, never instead of them. It never stops the shell that
# runs it: what it cannot do it says on stderr and skips. APPTOWN_TRUST_CERTS
# then names a file of the extra certificates alone (in a build, until the
# RUN line's shell exits). Running it again is
# cheap and does only what is not done yet (a JVM installed since).
#
# The script reaches App Platform as an environment value, which the
# platform rewrites wherever a dollar sign is followed by a brace, so none
# is written below (TestTheScriptHasNoBraceExpansion).
apptown_trust() {
	case $- in *e*) _at_e=1 ;; *) _at_e= ;; esac
	case $- in *u*) _at_u=1 ;; *) _at_u= ;; esac
	set +eu
	# The certificates, each marker on a line of its own, so the PEM reads as
	# PEM inside the script too (the gate reads them out of it).
	_at_pem='
@APPTOWN_TRUST_PEM@
'
	_apptown_trust
	[ -z "$_at_u" ] || set -u
	[ -z "$_at_e" ] || set -e
	return 0
}

_apptown_say() { echo "apptown: $*" >&2; }

_apptown_trust() {
	# A build writes into an image layer, so it keeps everything in a folder
	# removed when the RUN line's shell exits, and changes no file of the
	# image's.
	_at_build=
	[ -f /run/secrets/apptown-trust ] && _at_build=1
	if [ -n "$_at_build" ]; then
		if [ -z "$_at_dir" ] || [ ! -d "$_at_dir" ]; then
			_at_dir=$(mktemp -d 2>/dev/null) || { _apptown_say "no temporary folder for the extra certificates"; return; }
			trap 'rm -rf "$_at_dir"' EXIT
		fi
	else
		_at_dir=$TMPDIR
		[ -n "$_at_dir" ] || _at_dir=/tmp
		_at_dir=$_at_dir/apptown-trust-$(id -u 2>/dev/null || echo 0)
		mkdir -p "$_at_dir" 2>/dev/null || { _apptown_say "cannot write $_at_dir; the extra certificates are not trusted"; return; }
	fi
	# Readable by anyone: apt fetches as its own user.
	chmod 755 "$_at_dir" 2>/dev/null
	if [ ! -s "$_at_dir/extras.pem" ]; then
		printf '%s\n' "$_at_pem" >"$_at_dir/extras.pem" || { _apptown_say "cannot write $_at_dir"; return; }
		# One certificate per file: the system store and NSS take them so.
		_at_n=0 _at_f=
		while IFS= read -r _at_l; do
			case $_at_l in *'-----BEGIN CERTIFICATE-----'*)
				_at_n=$((_at_n + 1)) _at_f=$_at_dir/extra-$_at_n.crt
				: >"$_at_f" ;;
			esac
			[ -z "$_at_f" ] || printf '%s\n' "$_at_l" >>"$_at_f"
			case $_at_l in *'-----END CERTIFICATE-----'*) _at_f= ;; esac
		done <"$_at_dir/extras.pem"
	fi
	# The image's roots, as one file, and with the extras.
	_at_sys=
	for _at_f in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/ca-bundle.pem \
		/etc/pki/tls/cacert.pem /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem /etc/ssl/cert.pem; do
		if grep -q 'BEGIN CERTIFICATE' "$_at_f" 2>/dev/null; then _at_sys=$_at_f; break; fi
	done
	if [ ! -s "$_at_dir/roots.pem" ]; then
		{ [ -z "$_at_sys" ] || { cat "$_at_sys"; echo; }; cat "$_at_dir/extras.pem"; } >"$_at_dir/roots.pem" 2>/dev/null
	fi
	# Each variable that names a file of roots replaces the program's own:
	# it is set to the roots it would have used plus the extras. Those are
	# the file the image itself set it to, else the image's system roots.
	for _at_v in SSL_CERT_FILE CURL_CA_BUNDLE REQUESTS_CA_BUNDLE PIP_CERT GIT_SSL_CAINFO CARGO_HTTP_CAINFO AWS_CA_BUNDLE DENO_CERT; do
		eval "_at_cur=\$$_at_v"
		case $_at_cur in "$_at_dir"/*) continue ;; esac
		_at_out=$_at_dir/roots.pem
		if [ -n "$_at_cur" ] && [ -f "$_at_cur" ]; then
			_at_out=$_at_dir/$_at_v.pem
			[ -s "$_at_out" ] || { cat "$_at_cur"; echo; cat "$_at_dir/extras.pem"; } >"$_at_out" 2>/dev/null
		fi
		export "$_at_v=$_at_out"
	done
	# The extras alone, for a build whose final image has no shell to run
	# this script: it copies the file into that image where its TLS library
	# reads extra roots (the go starter's Dockerfile).
	export APPTOWN_TRUST_CERTS="$_at_dir/extras.pem"
	# Node adds this file to its own roots.
	case $NODE_EXTRA_CA_CERTS in
	"$_at_dir"/*) ;;
	'') export NODE_EXTRA_CA_CERTS="$_at_dir/extras.pem" ;;
	*)
		[ -s "$_at_dir/node-extra.pem" ] || { cat "$NODE_EXTRA_CA_CERTS"; echo; cat "$_at_dir/extras.pem"; } >"$_at_dir/node-extra.pem" 2>/dev/null
		export NODE_EXTRA_CA_CERTS="$_at_dir/node-extra.pem"
		;;
	esac
	# apt reads only its own setting (or the system store): a configuration
	# of its own, which includes the one the image named, if any.
	if command -v apt-get >/dev/null 2>&1; then
		case $APT_CONFIG in
		"$_at_dir"/*) ;;
		*)
			{ [ -z "$APT_CONFIG" ] || echo "#include \"$APT_CONFIG\";"; echo "Acquire::https::CAInfo \"$_at_dir/roots.pem\";"; } >"$_at_dir/apt.conf" &&
				export APT_CONFIG="$_at_dir/apt.conf"
			;;
		esac
	fi
	chmod 644 "$_at_dir"/*.pem "$_at_dir"/*.crt "$_at_dir"/apt.conf 2>/dev/null
	if [ -n "$_at_build" ]; then
		_apptown_trust_java
		return
	fi
	# The system store itself, for programs that read nothing else (GnuTLS
	# clients): only as root, and never in a build.
	if [ -n "$_at_sys" ] && [ "$(id -u 2>/dev/null)" = 0 ] && [ ! -f "$_at_dir/system-done" ]; then
		if { echo; cat "$_at_dir/extras.pem"; } >>"$_at_sys" 2>/dev/null; then
			: >"$_at_dir/system-done"
		else
			_apptown_say "cannot add the extra certificates to $_at_sys"
		fi
	fi
	_apptown_trust_java
	# Chromium's store.
	if command -v certutil >/dev/null 2>&1 && [ -n "$HOME" ] && [ ! -f "$_at_dir/nss-done" ]; then
		for _at_db in "$HOME/.pki/nssdb" "$HOME/.local/share/pki/nssdb"; do
			mkdir -p "$_at_db" 2>/dev/null || continue
			[ -f "$_at_db/cert9.db" ] || certutil -d "sql:$_at_db" -N --empty-password >/dev/null 2>&1
			_at_i=1
			while [ -f "$_at_dir/extra-$_at_i.crt" ]; do
				certutil -d "sql:$_at_db" -A -t C,, -n "apptown-$_at_i" -i "$_at_dir/extra-$_at_i.crt" >/dev/null 2>&1 ||
					_apptown_say "cannot add certificate $_at_i to $_at_db"
				_at_i=$((_at_i + 1))
			done
		done
		: >"$_at_dir/nss-done"
	fi
}

# Java reads its own store: jssecacerts when the JVM has one, else cacerts.
# A copy of that store with the extras is named in JAVA_TOOL_OPTIONS, which
# every JVM reads (Maven's and Gradle's too); the JVM's own file is never
# changed. A store the image already named is its policy and is left alone.
_apptown_trust_java() {
	case $JAVA_TOOL_OPTIONS in
	*"$_at_dir/java-cacerts"*) return ;;
	*javax.net.ssl.trustStore=*)
		_apptown_say "JAVA_TOOL_OPTIONS already names a trust store; Java is not given the extra certificates"
		return
		;;
	esac
	command -v java >/dev/null 2>&1 || return
	if [ ! -s "$_at_dir/java-cacerts" ]; then
		_at_home=$(java -XshowSettings:properties -version 2>&1 | sed -n 's/^ *java\.home = //p' | head -n 1)
		_at_store=$_at_home/lib/security/jssecacerts
		[ -f "$_at_store" ] || _at_store=$_at_home/lib/security/cacerts
		_at_kt=$_at_home/bin/keytool
		[ -x "$_at_kt" ] || _at_kt=$(command -v keytool 2>/dev/null)
		if [ -z "$_at_home" ] || [ ! -f "$_at_store" ] || [ -z "$_at_kt" ]; then
			_apptown_say "found java but not its trust store and keytool; Java is not given the extra certificates"
			return
		fi
		cp "$_at_store" "$_at_dir/java-cacerts.new" 2>/dev/null || { _apptown_say "cannot copy Java's trust store"; return; }
		chmod 644 "$_at_dir/java-cacerts.new" 2>/dev/null
		_at_i=1
		while [ -f "$_at_dir/extra-$_at_i.crt" ]; do
			"$_at_kt" -importcert -noprompt -keystore "$_at_dir/java-cacerts.new" -storepass changeit \
				-alias "apptown-$_at_i" -file "$_at_dir/extra-$_at_i.crt" >/dev/null 2>&1 || {
				_apptown_say "keytool could not add certificate $_at_i; Java is not given the extra certificates"
				rm -f "$_at_dir/java-cacerts.new"
				return
			}
			_at_i=$((_at_i + 1))
		done
		mv "$_at_dir/java-cacerts.new" "$_at_dir/java-cacerts" || return
	fi
	_at_jo=$JAVA_TOOL_OPTIONS
	[ -z "$_at_jo" ] || _at_jo="$_at_jo "
	export JAVA_TOOL_OPTIONS="$_at_jo-Djavax.net.ssl.trustStore=$_at_dir/java-cacerts -Djavax.net.ssl.trustStorePassword=changeit"
}

apptown_trust
