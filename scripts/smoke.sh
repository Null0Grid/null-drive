#!/usr/bin/env bash
# Null Drive smoke test: starts the server against a fresh data dir and walks
# the whole v1 API with curl. Doubles as living documentation of the API.
#
# Usage: scripts/smoke.sh [path/to/nulldrive-binary]     (needs curl and jq)
set -euo pipefail

BIN="${1:-bin/nulldrive}"
PORT="${NULLDRIVE_SMOKE_PORT:-18080}"
BASE="http://127.0.0.1:${PORT}/api/v1"

command -v curl >/dev/null || { echo "curl is required"; exit 1; }
command -v jq   >/dev/null || { echo "jq is required"; exit 1; }
[[ -x "$BIN" ]] || { echo "binary $BIN not found; run 'make build' first"; exit 1; }

DATA="$(mktemp -d)"
LOG="$DATA/server.log"
cleanup() {
  [[ -n "${PID:-}" ]] && kill "$PID" 2>/dev/null && wait "$PID" 2>/dev/null || true
  rm -rf "$DATA"
}
trap cleanup EXIT

step() { printf '\n\033[1;35m==> %s\033[0m\n' "$*"; }
ok()   { printf '    \033[32mok\033[0m %s\n' "$*"; }
fail() { printf '    \033[31mFAIL\033[0m %s\n' "$*"; echo "--- server log ---"; cat "$LOG"; exit 1; }

# expect STATUS METHOD PATH [curl args...] -> body in $BODY, headers in $HDRS
expect() {
  local want="$1" method="$2" path="$3"; shift 3
  local hdrfile="$DATA/headers"
  local code
  code=$(curl -sS -o "$DATA/body" -D "$hdrfile" -w '%{http_code}' -X "$method" "$@" "$BASE$path")
  BODY="$(cat "$DATA/body")"
  HDRS="$(cat "$hdrfile")"
  [[ "$code" == "$want" ]] || fail "$method $path -> $code, want $want: $BODY"
  ok "$method $path -> $code"
}
header() { printf '%s' "$HDRS" | tr -d '\r' | awk -v h="$(echo "$1" | tr '[:upper:]' '[:lower:]')" -F': ' 'tolower($1)==h {print $2}'; }

step "Starting server (data dir: $DATA)"
NULLDRIVE_DATA_DIR="$DATA" NULLDRIVE_PORT="$PORT" NULLDRIVE_LOG_LEVEL=debug "$BIN" >"$LOG" 2>&1 &
PID=$!
for _ in $(seq 1 50); do
  curl -fsS "$BASE/health" >/dev/null 2>&1 && break
  sleep 0.1
done
expect 200 GET /health
echo "    $BODY"

step "First-run setup"
expect 200 GET /setup/status
[[ "$(jq -r .setup_complete <<<"$BODY")" == "false" ]] || fail "fresh server says setup is complete"
expect 201 POST /setup -H 'Content-Type: application/json' -d '{"username":"admin","password":"correct horse battery"}'
SESSION="$(jq -r .token <<<"$BODY")"
expect 409 POST /setup -H 'Content-Type: application/json' -d '{"username":"evil","password":"takeover123"}'
ok "setup cannot be repeated"

step "Login / logout"
expect 401 POST /auth/login -d '{"username":"admin","password":"wrong-password"}'
expect 200 POST /auth/login -d '{"username":"admin","password":"correct horse battery"}'
SECOND="$(jq -r .token <<<"$BODY")"
expect 204 POST /auth/logout -H "Authorization: Bearer $SECOND"
expect 401 GET /me -H "Authorization: Bearer $SECOND"

step "Register a device"
expect 201 POST /devices -H "Authorization: Bearer $SESSION" -d '{"name":"Smoke Test PC"}'
DEVICE="$(jq -r .token <<<"$BODY")"
AUTH=(-H "Authorization: Bearer $DEVICE")
expect 200 GET /me "${AUTH[@]}"
[[ "$(jq -r .device.name <<<"$BODY")" == "Smoke Test PC" ]] || fail "device not recognised"
expect 403 POST /devices "${AUTH[@]}" -d '{"name":"nope"}'
ok "device tokens cannot mint devices"

step "Upload, list, download"
expect 200 GET /files "${AUTH[@]}"
START="$(jq -r .version <<<"$BODY")"
printf 'hello from null drive\n' > "$DATA/hello.txt"
SUM="$(sha256sum "$DATA/hello.txt" | cut -d' ' -f1)"
expect 201 PUT "/files/notes/hello.txt" "${AUTH[@]}" -H "X-Content-SHA256: $SUM" --data-binary "@$DATA/hello.txt"
V1="$(jq -r .version <<<"$BODY")"
expect 200 GET /files "${AUTH[@]}"
[[ "$(jq -r '[.files[] | select(.is_dir | not) | .path] | first' <<<"$BODY")" == "notes/hello.txt" ]] || fail "listing missing file"
[[ "$(jq -r '[.files[] | select(.is_dir) | .path] | first' <<<"$BODY")" == "notes" ]] || fail "parent folder not created"

step "Folders"
expect 201 POST /folders "${AUTH[@]}" -d '{"path":"Empty/Nested"}'
expect 200 POST /folders "${AUTH[@]}" -d '{"path":"Empty/Nested"}'
expect 409 PUT "/files/Empty/Nested" "${AUTH[@]}" --data-binary 'x'
expect 400 GET "/files/Empty" "${AUTH[@]}"
expect 200 DELETE "/files/Empty" "${AUTH[@]}"
expect 200 GET "/files/notes/hello.txt" "${AUTH[@]}"
[[ "$BODY" == "hello from null drive" ]] || fail "downloaded content differs: $BODY"
[[ "$(header etag)" == "\"$V1\"" ]] || fail "ETag $(header etag) != \"$V1\""

step "Optimistic concurrency"
expect 200 PUT "/files/notes/hello.txt" "${AUTH[@]}" -H "If-Match: \"$V1\"" --data-binary 'edited on device A'
expect 409 PUT "/files/notes/hello.txt" "${AUTH[@]}" -H "If-Match: \"$V1\"" --data-binary 'edited on device B'
[[ "$(jq -r .error.code <<<"$BODY")" == "conflict" ]] || fail "expected conflict body"
ok "stale edit rejected with current version $(jq -r .error.current.version <<<"$BODY")"
expect 428 PUT "/files/notes/hello.txt" "${AUTH[@]}" --data-binary 'blind overwrite'
ok "overwrite without If-Match refused"

step "Path safety"
expect 400 PUT "/files/../../etc/passwd" "${AUTH[@]}" --path-as-is --data-binary 'x'
expect 400 PUT "/files/%2e%2e/secret" "${AUTH[@]}" --data-binary 'x'

step "Move and delete"
expect 200 POST /files/move "${AUTH[@]}" -d '{"from":"notes","to":"archive/notes"}'
expect 200 GET "/files/archive/notes/hello.txt" "${AUTH[@]}"
expect 200 DELETE "/files/archive/notes/hello.txt" "${AUTH[@]}"
expect 404 GET "/files/archive/notes/hello.txt" "${AUTH[@]}"

step "Change feed includes tombstones"
expect 200 GET "/files?since=$START" "${AUTH[@]}"
TOMBS="$(jq '[.files[] | select(.deleted)] | length' <<<"$BODY")"
[[ "$TOMBS" -ge 2 ]] || fail "expected tombstones for moved+deleted paths, got $TOMBS"
ok "$TOMBS tombstones since version $START"

step "Revoke device"
DEVICE_ID="$(curl -sS -H "Authorization: Bearer $SESSION" "$BASE/devices" | jq -r '.devices[0].id')"
expect 200 DELETE "/devices/$DEVICE_ID" -H "Authorization: Bearer $SESSION"
expect 401 GET /files "${AUTH[@]}"

step "Pairing code: claim once, then refused"
expect 201 POST /pairing -H "Authorization: Bearer $SESSION" -d '{}'
CODE="$(jq -r .code <<<"$BODY")"
[[ "$CODE" =~ ^NULL-[0-9A-Z]{4}-[0-9A-Z]{2}$ ]] || fail "unexpected code format: $CODE"
expect 201 POST /pairing/claim -d "{\"code\":\"$CODE\",\"device_name\":\"smoke-paired\"}"
PAIRED="$(jq -r .token <<<"$BODY")"
expect 200 GET /files -H "Authorization: Bearer $PAIRED"
expect 400 POST /pairing/claim -d "{\"code\":\"$CODE\",\"device_name\":\"again\"}"

step "Realtime endpoint and server settings"
expect 426 GET /ws -H "Authorization: Bearer $PAIRED"
expect 200 GET /system/settings -H "Authorization: Bearer $SESSION"
ok "mDNS active: $(jq -r .mdns_active <<<"$BODY") $(jq -c .mdns_addrs <<<"$BODY")"

step "API v2: chunked upload, history, restore"
# expect() prefixes /api/v1; "/../v2" reaches /api/v2 because curl resolves
# dot segments before sending (RFC 3986), so the request path is clean.
CHUNK="$DATA/chunk.bin"
printf 'chunked hello from smoke' > "$CHUNK"
H="$(sha256sum "$CHUNK" | cut -d' ' -f1)"
expect 200 POST /../v2/chunks/check -H "Authorization: Bearer $SESSION" -d "{\"hashes\":[\"$H\"]}"
[[ "$(jq -r '.missing | length' <<<"$BODY")" == "1" ]] || fail "chunk should be missing"
expect 201 PUT "/../v2/chunks/$H" -H "Authorization: Bearer $SESSION" --data-binary "@$CHUNK"
expect 201 PUT /../v2/files/v2/doc.txt -H "Authorization: Bearer $SESSION" \
  -d "{\"size\":24,\"sha256\":\"$H\",\"chunks\":[{\"hash\":\"$H\",\"size\":24}]}"
V1="$(jq -r .version <<<"$BODY")"
expect 200 GET /files/v2/doc.txt -H "Authorization: Bearer $SESSION"
[[ "$BODY" == "chunked hello from smoke" ]] || fail "v1 download of a v2 file: $BODY"
expect 200 PUT /files/v2/doc.txt -H "Authorization: Bearer $SESSION" -H "If-Match: \"$V1\"" --data-binary 'second version'
expect 200 GET /../v2/files/v2/doc.txt/versions -H "Authorization: Bearer $SESSION"
[[ "$(jq '.versions | length' <<<"$BODY")" == "2" ]] || fail "expected 2 versions"
expect 200 POST "/../v2/files/v2/doc.txt/versions/$V1/restore" -H "Authorization: Bearer $SESSION"
expect 200 GET /files/v2/doc.txt -H "Authorization: Bearer $SESSION"
[[ "$BODY" == "chunked hello from smoke" ]] || fail "restore failed: $BODY"
expect 200 DELETE /files/v2/doc.txt -H "Authorization: Bearer $SESSION"
expect 200 GET /../v2/trash -H "Authorization: Bearer $SESSION"
[[ "$(jq -r '.items[0].path' <<<"$BODY")" == "v2/doc.txt" ]] || fail "not in trash"
expect 200 GET /../v2/stats -H "Authorization: Bearer $SESSION"
ok "stats: $(jq -c '{history_bytes, stored_bytes, versions}' <<<"$BODY")"
expect 200 POST /../v2/admin/gc -H "Authorization: Bearer $SESSION"

step "Security headers, CSRF and login lockout (Phase 7)"
expect 200 GET /health
[[ "$(header cache-control)" == "no-store" ]] || fail "API responses must be no-store"
[[ "$(header content-security-policy)" == *"default-src 'none'"* ]] || fail "API CSP missing"
JAR="$DATA/cookies"
expect 200 POST /auth/login -c "$JAR" -d '{"username":"admin","password":"correct horse battery"}'
CSRF="$(jq -r .csrf_token <<<"$BODY")"
expect 403 POST /devices -b "$JAR" -d '{"name":"forged"}'
ok "cookie without CSRF token refused"
expect 201 POST /devices -b "$JAR" -H "X-NullDrive-CSRF: $CSRF" -d '{"name":"browser-made"}'
expect 403 POST /auth/login -H 'Origin: https://evil.example' -d '{"username":"admin","password":"correct horse battery"}'
ok "cross-origin login refused"
expect 200 GET /admin/audit -H "Authorization: Bearer $SESSION"
[[ "$(jq -r '[.events[].event] | index("login_failed") != null' <<<"$BODY")" == "true" ]] || fail "audit log lacks the failed login"
ok "audit log: $(jq -c '[.events[].event] | unique' <<<"$BODY")"

step "End-to-end encryption via the CLI (Phase 7)"
E2EE_CLI="${NULLDRIVE_E2EE_CLI:-$(dirname "$BIN")/nulldrive-e2ee}"
if [[ -x "$E2EE_CLI" ]]; then
  export NULLDRIVE_SERVER="http://127.0.0.1:${PORT}" NULLDRIVE_USER=admin NULLDRIVE_PASSWORD="correct horse battery"
  export NULLDRIVE_E2EE_PASSPHRASE="smoke test encryption passphrase"
  expect 200 GET /files -H "Authorization: Bearer $SESSION"
  NAMES="$(jq -r '.files[].path' <<<"$BODY" | tr '/' '\n' | sort -u | grep -E '.{6,}' || true)"
  [[ -n "$NAMES" ]] || fail "no file names to check for"
  "$E2EE_CLI" status | grep -q 'state: off' || fail "CLI status"
  "$E2EE_CLI" enable > "$DATA/enable.out" 2>&1 || { cat "$DATA/enable.out"; fail "CLI enable"; }
  grep -qE '([0-9A-Z]{4}-){7}[0-9A-Z]{4}' "$DATA/enable.out" || fail "no recovery key printed"
  ok "encryption on; recovery key printed; existing files migrated"
  "$E2EE_CLI" status | grep -q 'state: on' || fail "state not on"
  "$E2EE_CLI" verify | grep -q '^OK' || fail "passphrase does not unlock"
  expect 409 GET /files -H "Authorization: Bearer $SESSION"
  ok "clients without E2EE support are refused"
  expect 200 GET /files -H "Authorization: Bearer $SESSION" -H 'X-NullDrive-E2EE: 1'
  [[ "$(jq '[.files[].path | select(startswith("~") | not)] | length' <<<"$BODY")" == "0" ]] || fail "plaintext path on server"
  expect 409 PUT /files/plain.txt -H "Authorization: Bearer $SESSION" -H 'X-NullDrive-E2EE: 1' --data-binary 'x'
  ok "plaintext uploads refused"
  while IFS= read -r n; do
    [[ -z "$n" ]] && continue
    if grep -rqaF -- "$n" "$DATA"/nulldrive.db* "$DATA"/blobs 2>/dev/null; then fail "plaintext name '$n' still in the data dir"; fi
  done <<<"$NAMES"
  ok "no file names left in the database or blobs"
else
  echo "    (skipped: $E2EE_CLI not built; 'make build' builds it)"
fi

step "Web UI and system info"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/files/anything")
[[ "$code" == "200" ]] || fail "SPA route returned $code"
ok "GET /files/anything -> 200 (app shell or 'UI not built' page)"
expect 200 GET /system/info -H "Authorization: Bearer $SESSION"
expect 401 GET /system/info

step "Graceful shutdown"
kill -TERM "$PID"
wait "$PID" && ok "server exited cleanly"
PID=""
grep -q '"msg":"stopped"' "$LOG" || fail "no clean shutdown log line"

printf '\n\033[1;32mSmoke test passed.\033[0m\n'
