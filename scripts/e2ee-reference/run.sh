#!/usr/bin/env bash
# Runs the independent Python E2EE client (written from docs/NOTES-E2EE.md):
# 1. the test vectors shared with Go and the web UI, 2. the Null Notes flow
# against a fresh server. Usage: scripts/e2ee-reference/run.sh [bin/nulldrive]
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="${1:-bin/nulldrive}"
PORT="${NULLDRIVE_E2EE_REF_PORT:-18082}"
python3 -c 'import nacl' 2>/dev/null || { echo "needs PyNaCl: pip install pynacl"; exit 1; }
[[ -x "$BIN" ]] || { echo "binary $BIN not found; run 'make build' first"; exit 1; }

python3 "$HERE/nulldrive_e2ee.py" vectors

DATA="$(mktemp -d)"
cleanup() { [[ -n "${PID:-}" ]] && kill "$PID" 2>/dev/null; wait "${PID:-}" 2>/dev/null || true; rm -rf "$DATA"; }
trap cleanup EXIT
NULLDRIVE_DATA_DIR="$DATA" NULLDRIVE_PORT="$PORT" "$BIN" >"$DATA/server.log" 2>&1 &
PID=$!
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1 && break; sleep 0.1; done
python3 "$HERE/nulldrive_e2ee.py" live "http://127.0.0.1:$PORT" || { cat "$DATA/server.log"; exit 1; }
