#!/usr/bin/env bash
# End-to-end rehearsal of people + messages backup and restore with the built binary.
# Seeds a scratch instance through the API, seals both capsules to a throwaway 2-of-3
# key, restores into an empty directory with the shares on stdin and checks the
# restored device is suspended. Loopback and mktemp scratch only; safe to re-run.
# Usage: scripts/restore-messages-rehearsal.sh
set -euo pipefail

for tool in go curl jq python3; do
  command -v "$tool" >/dev/null || { echo "rehearsal needs $tool on PATH" >&2; exit 1; }
done

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d -t kymessages-rehearsal-XXXXXX)"
SERVER_PID=""

cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || :
    wait "$SERVER_PID" 2>/dev/null || :
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() { printf '  [FAIL] %s\n' "$1"; exit 1; }
pass() { printf '  [ok]   %s\n' "$1"; }

PORT="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
BASE="http://127.0.0.1:${PORT}"
ADMIN_PASS="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
USER_ID="rehearsal-alice"

# A clean environment: no live recovery URL, identity issuer or data dir leaks in.
run() { # run <data-dir> <backup-dir> <command...>
  local data="$1" backups="$2"
  shift 2
  env -i PATH="$PATH" HOME="$WORK" KY_HOST=127.0.0.1 KY_PORT="$PORT" KY_APP_URL="$BASE" \
    KY_DATA_DIR="$data" KY_BACKUP_DIR="$backups" KY_DB_DRIVER=sqlite \
    KY_ADMIN_PASSWORD="$ADMIN_PASS" KY_CAPTCHA_PROVIDER=none "$@"
}

start_server() { # start_server <data-dir> <backup-dir> <log>
  run "$1" "$2" "$WORK/kymessages" >"$3" 2>&1 &
  SERVER_PID=$!
  curl -s -o /dev/null --retry 30 --retry-delay 1 --retry-all-errors "$BASE/api/settings" ||
    { cat "$3"; fail "server did not come up"; }
  kill -0 "$SERVER_PID" 2>/dev/null || { cat "$3"; fail "server exited during startup"; }
}

stop_server() {
  kill "$SERVER_PID"
  wait "$SERVER_PID" || fail "unclean shutdown"
  SERVER_PID=""
}

echo "==> Build"
(cd "$REPO" && go build -o "$WORK/kymessages" ./cmd/server && go build -tags rehearsal -o "$WORK/rehearsal" ./scripts/rehearsal)
"$WORK/rehearsal" keygen -out "$WORK"
pass "throwaway 2-of-3 recovery key; two shares in a 0600 scratch file"

SRC="$WORK/source"
echo "==> Seed via the API"
start_server "$SRC/data" "$SRC/backups" "$WORK/source.log"
REHEARSAL_ADMIN_PASSWORD="$ADMIN_PASS" "$WORK/rehearsal" pin -url "$BASE" -dir "$WORK"
pass "admin replaced the bootstrap password and pinned the key by hand"
SESSION="$("$WORK/rehearsal" session -data "$SRC/data" -user "$USER_ID")"
"$WORK/rehearsal" seed -url "$BASE" -session "$SESSION" -dir "$WORK"
pass "device enrolled and verified; room created; commit and application events appended"
stop_server

echo "==> Seal"
run "$SRC/data" "$SRC/backups" "$WORK/kymessages" export-capsule -out "$WORK/people.kycap" 2>&1 | tail -1
run "$SRC/data" "$SRC/backups" "$WORK/kymessages" deposit -messages 2>&1 | tail -1
shopt -s nullglob
MESSAGES=("$SRC/backups/messages/"*.kycap)
shopt -u nullglob
[ "${#MESSAGES[@]}" -eq 1 ] || fail "expected one messages capsule, found ${#MESSAGES[@]}"
pass "people capsule exported; messages capsule written to the local directory"

DST="$WORK/restored"
mkdir -m 0700 "$DST"
echo "==> Restore"
run "$DST/data" "$DST/backups" "$WORK/kymessages" restore -capsule "$WORK/people.kycap" -to "$DST" <"$WORK/shares"
run "$DST/data" "$DST/backups" "$WORK/kymessages" restore-messages -capsule "${MESSAGES[0]}" -into "$DST" <"$WORK/shares"
pass "restore and restore-messages read the shares from stdin"

echo "==> Restored server"
start_server "$DST/data" "$DST/backups" "$WORK/restored.log"
SESSION="$("$WORK/rehearsal" session -data "$DST/data" -user "$USER_ID")"
STATUSES="$(curl -sf -H "Authorization: Bearer $SESSION" "$BASE/api/messaging/devices" | jq -r '[.devices[].status] | join(",")')"
[ "$STATUSES" = "suspended" ] || fail "devices report '$STATUSES', want 'suspended'"
pass "/api/messaging/devices reports the restored device as suspended"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SESSION" \
  -H "X-KyMessages-Device: $(cat "$WORK/device-token")" "$BASE/api/messaging/rooms/$(cat "$WORK/room-id")/delivery")"
[ "$CODE" = "403" ] || fail "old device token got $CODE, want 403"
pass "the pre-backup device token no longer authenticates (403)"
stop_server

echo "rehearsal: all checks passed"
