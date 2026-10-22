#!/usr/bin/env bash
# Records a product video and encodes it as an MP4 with chapters:
#
#   scripts/record-video.sh demo     → docs/video/null-drive-feature-demo.mp4
#   scripts/record-video.sh setup    → docs/video/null-drive-server-setup.mp4
#   (each with a <name>.chapters.txt: YouTube-style "0:00 Title" lines)
#
# The scenes are ui/e2e/video/*.spec.ts. They run against the real server
# (Playwright starts bin/nulldrive on a fresh data dir) on a virtual screen,
# 1600×900, which ffmpeg films. The demo also films the real Linux desktop
# app syncing beside the browser: NULLDRIVE_APPIMAGE=path/to/NullDrive-*.AppImage.
#
# Needs: make build (bin/nulldrive), the ui's npm packages, ffmpeg, xvfb-run,
# dbus-run-session, gnome-keyring, libsecret-tools, xdotool, python3.
# PW_CHROMIUM=/path/to/chrome uses that Chromium instead of Playwright's.
set -euo pipefail
cd "$(dirname "$0")/.."

NAME="${1:?usage: record-video.sh demo|setup}"
case "$NAME" in
  demo) PROJECT=video-demo OUT=null-drive-feature-demo ;;
  setup) PROJECT=video-setup OUT=null-drive-server-setup ;;
  *) echo "usage: record-video.sh demo|setup" >&2; exit 2 ;;
esac

# A virtual screen and a private D-Bus session (for the keyring and the app).
if [[ -z "${NDV_INSIDE:-}" ]]; then
  exec env NDV_INSIDE=1 xvfb-run -a -s "-screen 0 1600x900x24" dbus-run-session -- "$0" "$@"
fi

# Short path: gnome-keyring's control socket must fit in 108 bytes.
WORK="$(mktemp -d /tmp/ndv.XXXXXX)"
export NDV_HOME="$WORK/h"
mkdir -p "$NDV_HOME" docs/video
FF=""
cleanup() {
  if [[ -n "$FF" ]]; then kill -INT "$FF" 2>/dev/null || true; fi
  pkill -f "$WORK" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# The keyring, unlocked, as on a desktop session.
(export HOME="$NDV_HOME"; echo -n video | gnome-keyring-daemon --unlock --components=secrets >/dev/null)
gdbus call --session --dest org.freedesktop.secrets --object-path /org/freedesktop/secrets \
  --method org.freedesktop.Secret.Service.SetAlias default /org/freedesktop/secrets/collection/login >/dev/null

# Film the screen (lossless-ish; the final encode is below).
ffmpeg -v error -y -f x11grab -video_size 1600x900 -framerate 30 -draw_mouse 0 -i "$DISPLAY" \
  -c:v libx264 -preset ultrafast -crf 10 -pix_fmt yuv444p "$WORK/raw.mkv" &
FF=$!
sleep 1

RESULTS="ui/test-results/$PROJECT"
RAW="$RESULTS/raw.mkv"
if [[ -n "${REUSE:-}" && -f "$RAW" ]]; then
  # REUSE=1: encode the last recording again (after changing this script).
  kill -INT "$FF"; wait "$FF" || true; FF=""
else
  rm -rf "$RESULTS"
  (cd ui && npx playwright test --project="$PROJECT")
  kill -INT "$FF"
  wait "$FF" || true
  FF=""
  mv "$WORK/raw.mkv" "$RAW"
fi

# Find each chapter's corner marker (top-left 8×8 px, averaged) at 30 fps.
ffmpeg -v error -i "$RAW" -vf "fps=30,crop=8:8:0:0,scale=1:1:flags=area" -f rawvideo -pix_fmt rgb24 - |
python3 -c '
import json, sys
meta = json.load(open(sys.argv[1]))
raw = sys.stdin.buffer.read()
px = [tuple(raw[i:i+3]) for i in range(0, len(raw), 3)]
def rgb(h): return tuple(int(h[i:i+2], 16) for i in (1, 3, 5))
times, pos = [], 0
for m in meta["markers"]:
    want = rgb(m)
    hit = next((i for i in range(pos, len(px)) if sum(abs(a - b) for a, b in zip(px[i], want)) < 90), None)
    if hit is None:
        sys.exit(f"marker {m} not found in the recording")
    times.append(hit / 30)
    pos = hit + 30
start, end = times[0], times[-1]
with open(sys.argv[2], "w") as f:
    f.write(";FFMETADATA1\ntitle=" + meta["title"] + "\n")
    for i, title in enumerate(meta["chapters"]):
        s, e = times[i] - start, times[i + 1] - start
        f.write(f"[CHAPTER]\nTIMEBASE=1/1000\nSTART={int(s*1000)}\nEND={int(e*1000)}\ntitle={title}\n")
with open(sys.argv[3], "w") as f:
    for i, title in enumerate(meta["chapters"]):
        s = int(times[i] - start)
        f.write(f"{s//60}:{s%60:02d} {title}\n")
with open(sys.argv[4], "w") as f:
    f.write(f"{start:.3f} {end - start:.3f}\n")
' "$RESULTS/chapters.json" "$WORK/chapters.ffmeta" "docs/video/$OUT.chapters.txt" "$WORK/span"
read -r START LEN <"$WORK/span"

# The chapters cover the video from the first marker to the end marker.
ffmpeg -v error -y -ss "$START" -t "$LEN" -i "$RAW" -i "$WORK/chapters.ffmeta" -map 0:v -map_metadata 1 -map_chapters 1 \
  -c:v libx264 -preset slow -crf 20 -pix_fmt yuv420p -movflags +faststart -an "docs/video/$OUT.mp4"
cat "docs/video/$OUT.chapters.txt"
echo "wrote docs/video/$OUT.mp4 ($(du -h "docs/video/$OUT.mp4" | cut -f1), ${LEN%.*} s)"
