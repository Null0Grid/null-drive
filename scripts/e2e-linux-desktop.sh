#!/usr/bin/env bash
# End-to-end test of the Linux desktop app: a real server, the Secret Service
# keyring (gnome-keyring), the app under a virtual display. Checks that a file
# on the server is downloaded, a local file is uploaded, and (for an AppImage)
# that the app registered itself as the nulldrive:// handler.
#
#   xvfb-run -a dbus-run-session -- scripts/e2e-linux-desktop.sh APP SERVER [screenshot.png]
#
# APP is an AppImage, an installed nulldrive-desktop, or flatpak:<app-id> for
# an installed Flatpak; SERVER is bin/nulldrive.
# Needs gnome-keyring, libsecret-tools (secret-tool), curl, python3.
set -euo pipefail

APP="${1:?usage: e2e-linux-desktop.sh APP SERVER [screenshot]}"
[[ "$APP" == flatpak:* ]] || APP="$(realpath "$APP")"
SERVER="$(realpath "${2:?usage: e2e-linux-desktop.sh APP SERVER [screenshot]}")"
SHOT="${3:-}"
PORT="${E2E_PORT:-18190}"
[[ -n "${DISPLAY:-}" && -n "${DBUS_SESSION_BUS_ADDRESS:-}" ]] ||
  { echo "run under xvfb-run and dbus-run-session (see the header)" >&2; exit 2; }

# A short path: gnome-keyring's control socket must fit in 108 bytes.
# E2E_TMP moves it out of /tmp, which Flatpak never shares with a sandbox.
T="$(mktemp -d "${E2E_TMP:-/tmp}/nde2e.XXXXXX")"
export HOME="$T/h" XDG_CONFIG_HOME="$T/h/.config" XDG_DATA_HOME="$T/h/.local/share"
# Where the app keeps its settings: Flatpak gives each app its own config dir.
APPCONF="$XDG_CONFIG_HOME/Null Drive"
[[ "$APP" == flatpak:* ]] && APPCONF="$HOME/.var/app/${APP#flatpak:}/config/Null Drive"
mkdir -p "$HOME" "$T/data"
PIDS=()
cleanup() {
  # Each runs in its own process group (setsid), so WebKit's helper
  # processes stop with the app.
  for p in "${PIDS[@]}"; do kill -- "-$p" 2>/dev/null || true; done
  sleep 1
  for p in "${PIDS[@]}"; do kill -9 -- "-$p" 2>/dev/null || true; done
  rm -rf "$T"
}
trap cleanup EXIT
fail() {
  echo "::error::linux desktop e2e: $*"
  for f in "$APPCONF/logs/client.log" "$T/app.log"; do
    [[ -f "$f" ]] && tail -n 15 "$f" | sed 's/^/  | /'
  done
  exit 1
}

# Keyring: unlocked "login" collection as the default, as on a desktop session.
echo -n e2e | timeout 10 gnome-keyring-daemon --unlock --components=secrets >/dev/null ||
  fail "gnome-keyring did not start"
timeout 10 gdbus call --session --dest org.freedesktop.secrets --object-path /org/freedesktop/secrets \
  --method org.freedesktop.Secret.Service.SetAlias default /org/freedesktop/secrets/collection/login >/dev/null ||
  fail "could not make the login keyring the default"

# Server with an admin, a device token and one file.
NULLDRIVE_DATA_DIR="$T/data" NULLDRIVE_PORT="$PORT" NULLDRIVE_MDNS=off setsid "$SERVER" >"$T/server.log" 2>&1 &
PIDS+=($!)
B="http://127.0.0.1:$PORT/api/v1"
for _ in $(seq 50); do curl -fsS "$B/health" >/dev/null 2>&1 && break; sleep 0.2; done
json() { python3 -c "import sys,json; d=json.load(sys.stdin); print(eval('d'+sys.argv[1]))" "$1"; }
SES=$(curl -fsS -X POST "$B/setup" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"correct horse battery"}' | json '["token"]') || fail "server setup"
DEV=$(curl -fsS -X POST "$B/devices" -H "Authorization: Bearer $SES" -H 'Content-Type: application/json' -d '{"name":"linux-e2e"}')
TOK=$(json '["token"]' <<<"$DEV"); DID=$(json '["device"]["id"]' <<<"$DEV")
curl -fsS -X PUT "$B/files/From%20server.txt" -H "Authorization: Bearer $SES" -H 'If-None-Match: *' \
  --data-binary "hello from the server" >/dev/null || fail "upload to the server"

# What the setup screens save: the config file, and the token in the keyring
# under the same service/account go-keyring uses.
SYNC="$HOME/NullDrive"
mkdir -p "$APPCONF" "$SYNC"
cat >"$APPCONF/config.json" <<EOF
{"server_url":"http://127.0.0.1:$PORT","username":"admin","device_name":"linux-e2e","device_id":$DID,"folder":"$SYNC","poll_seconds":2}
EOF
echo -n "$TOK" | timeout 10 secret-tool store --label="Null Drive" service "Null Drive" \
  username "http://127.0.0.1:$PORT|linux-e2e" || fail "secret-tool store"

# The app (an AppImage runs without FUSE this way).
if [[ "$APP" == flatpak:* ]]; then
  setsid flatpak run "${APP#flatpak:}" >"$T/app.log" 2>&1 &
else
  APPIMAGE_EXTRACT_AND_RUN=1 setsid "$APP" >"$T/app.log" 2>&1 &
fi
PIDS+=($!)

for _ in $(seq 60); do [[ -f "$SYNC/From server.txt" ]] && break; sleep 0.5; done
[[ "$(cat "$SYNC/From server.txt" 2>/dev/null)" == "hello from the server" ]] || fail "server file was not downloaded"
echo "ok   download"

echo "made on linux" >"$SYNC/From Linux.txt"
got=""
for _ in $(seq 60); do
  got=$(curl -fsS "$B/files/From%20Linux.txt" -H "Authorization: Bearer $SES" 2>/dev/null) && break; sleep 0.5
done
[[ "$got" == "made on linux" ]] || fail "local file was not uploaded"
echo "ok   upload"

if [[ "$APP" == *.AppImage ]]; then
  handler=$(xdg-mime query default x-scheme-handler/nulldrive 2>/dev/null || true)
  [[ "$handler" == nulldrive-desktop.desktop ]] || fail "nulldrive:// handler is '$handler'"
  grep -qF "Exec=\"$APP\" %u" "$XDG_DATA_HOME/applications/nulldrive-desktop.desktop" ||
    fail "the handler does not start the AppImage"
  echo "ok   nulldrive:// opens the AppImage"
fi

if [[ -n "$SHOT" ]] && command -v import >/dev/null; then
  sleep 1; import -window root "$SHOT" && echo "screenshot: $SHOT"
fi
echo "PASS linux desktop e2e"
