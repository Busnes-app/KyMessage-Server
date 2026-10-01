#!/usr/bin/env bash
# usage: kyid-admin.sh METHOD PATH [JSON]
# Calls the throwaway KyIdentity admin API as the bootstrap admin: signs in once (cookie jar),
# takes a step-up grant for this exact write, then makes the call. Prints the response
# body; exits non-zero on any HTTP error. Env (set by matrix-acceptance.sh):
#   KYID_URL         https://id.kymatrix.test
#   KYID_STATE       scratch directory for the cookie jar
#   KYID_ADMIN_PASS  bootstrap admin password (throwaway)
#   ACCEPT_CACERT    the harness's throwaway CA
#   ACCEPT_CONNECT   host:port of the harness TLS proxy on loopback
set -euo pipefail
method=$1 path=$2 body=${3:-}
jar="$KYID_STATE/kyid-admin.jar"

kcurl() {
	curl -sS --cacert "$ACCEPT_CACERT" --connect-to "::$ACCEPT_CONNECT" -c "$jar" -b "$jar" "$@"
}
csrf() { awk '$6 == "kyidentity_csrf" { v = $7 } END { print v }' "$jar"; }
# call METHOD PATH [extra curl args...]: fails on HTTP >= 400 and shows the body.
call() {
	local m=$1 p=$2 out code
	shift 2
	out=$(kcurl -X "$m" -w '\n%{http_code}' -H "X-CSRF-Token: $(csrf)" -H 'Content-Type: application/json' "$@" "$KYID_URL$p")
	code=${out##*$'\n'}
	out=${out%$'\n'*}
	if ((code >= 400)); then
		echo "kyid-admin: $m $p -> HTTP $code: $out" >&2
		return 1
	fi
	printf '%s\n' "$out"
}

if ! kcurl -sf -o /dev/null "$KYID_URL/api/auth/me" 2>/dev/null; then
	rm -f "$jar"
	kcurl -o /dev/null "$KYID_URL/api/auth/csrf"
	call POST /api/auth/login -d "$(jq -n --arg p "$KYID_ADMIN_PASS" '{username: "admin", password: $p}')" >/dev/null
fi
kcurl -o /dev/null "$KYID_URL/api/auth/csrf"
# Reads spend no grant; every admin write here is a step-up route.
[[ $method == GET ]] && { call GET "$path"; exit; }
grant=$(call POST /api/auth/step-up -d "$(jq -n --arg p "$KYID_ADMIN_PASS" --arg o "$method $path" '{password: $p, operation: $o}')" | jq -r '.stepUpToken // empty')
[[ -n $grant ]] || { echo "kyid-admin: no step-up grant for $method $path" >&2; exit 1; }
call "$method" "$path" -H "X-KyIdentity-StepUp: $grant" ${body:+-d "$body"}
