#!/usr/bin/env bash
# usage: kyid-admin.sh METHOD PATH [JSON]
#        kyid-admin.sh first-password USER PASSFILE
# Calls the throwaway KyIdentity admin API as the bootstrap admin: signs in once (cookie jar),
# takes a step-up grant for this exact write, then makes the call. Prints the response
# body; exits non-zero on any HTTP error. Env (set by matrix-acceptance.sh):
#   KYID_URL         https://id.kymatrix.test
#   KYID_STATE       scratch directory for the cookie jar
#   KYID_ADMIN_PASS  bootstrap admin password (throwaway); $KYID_STATE/admin.pass, when present,
#                    holds the one first-password chose instead
#   ACCEPT_CACERT    the harness's throwaway CA
#   ACCEPT_CONNECT   host:port of the harness TLS proxy on loopback
set -euo pipefail
method=$1 path=$2 body=${3:-}
jar="$KYID_STATE/kyid-admin.jar"
[[ -f $KYID_STATE/admin.pass ]] && KYID_ADMIN_PASS=$(cat "$KYID_STATE/admin.pass")

kcurl() {
	curl -sS --cacert "$ACCEPT_CACERT" --connect-to "::$ACCEPT_CONNECT" -c "$jar" -b "$jar" "$@"
}
csrf() { awk '$6 == "kyidentity_csrf" { v = $7 } END { print v }' "$jar"; }
# call METHOD PATH [extra curl args...]: fails on HTTP >= 400 and shows the body. A 429 is
# retried up to 5 times, 1s, 2s, 3s, 4s apart.
call() {
	local m=$1 p=$2 out code attempt
	shift 2
	for attempt in 1 2 3 4 5; do
		out=$(kcurl -X "$m" -w '\n%{http_code}' -H "X-CSRF-Token: $(csrf)" -H 'Content-Type: application/json' "$@" "$KYID_URL$p")
		code=${out##*$'\n'}
		out=${out%$'\n'*}
		((code == 429 && attempt < 5)) || break
		echo "kyid-admin: $m $p -> HTTP 429, retry $attempt" >&2
		sleep "$attempt"
	done
	if ((code >= 400)); then
		echo "kyid-admin: $m $p -> HTTP $code: $out" >&2
		return 1
	fi
	printf '%s\n' "$out"
}

# first-password USER PASSFILE: KyIdentity makes a password an administrator gave (the bootstrap
# admin's, every user this harness creates) usable only to choose a new one. Signs USER in and,
# when KyIdentity asks for it, replaces the password in PASSFILE with a new random one.
if [[ $method == first-password ]]; then
	user=$2 file=$3 jar="$KYID_STATE/kyid-first-$2.jar"
	pass=$(cat "$file")
	rm -f "$jar"
	kcurl -o /dev/null "$KYID_URL/api/auth/csrf"
	call POST /api/auth/login -d "$(jq -n --arg u "$user" --arg p "$pass" '{username: $u, password: $p}')" >/dev/null
	if call GET /api/auth/me | jq -e '[.. | .passwordChangeRequired? // empty] | any' >/dev/null; then
		grant=$(call POST /api/auth/step-up -d "$(jq -n --arg p "$pass" '{password: $p, operation: "POST /api/user/password"}')" | jq -r '.stepUpToken // empty')
		[[ -n $grant ]] || { echo "kyid-admin: no step-up grant for $user's password change" >&2; exit 1; }
		new=$(openssl rand -hex 16)
		call POST /api/user/password -H "X-KyIdentity-StepUp: $grant" -d "$(jq -n --arg c "$pass" --arg n "$new" '{currentPassword: $c, newPassword: $n}')" >/dev/null
		printf '%s\n' "$new" >"$file"
		echo "changed"
	fi
	call POST /api/auth/logout >/dev/null || true
	rm -f "$jar"
	exit
fi

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
