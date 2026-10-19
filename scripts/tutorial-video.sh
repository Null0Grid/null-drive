#!/usr/bin/env bash
# Turns the recorded tutorial (ui/e2e/tutorial.spec.ts) into an MP4 with
# chapters. Needs ffmpeg and python3.
#
#   make build && (cd ui && npx playwright test --project=tutorial) && scripts/tutorial-video.sh
#
# Output: docs/video/null-drive-getting-started.mp4 and chapters.txt
# (YouTube-style "0:00 Title" lines).
set -euo pipefail
cd "$(dirname "$0")/.."
IN=ui/test-results/tutorial/tutorial.webm
META=ui/test-results/tutorial/chapters.json
OUT=docs/video
mkdir -p "$OUT"
[[ -f "$IN" && -f "$META" ]] || { echo "record first: cd ui && npx playwright test --project=tutorial"; exit 1; }

# Find each chapter's corner marker: sample the top-left 8x8 px at 25 fps
# as one averaged RGB pixel per frame.
ffmpeg -v error -i "$IN" -vf "fps=25,crop=8:8:0:0,scale=1:1:flags=area" -f rawvideo -pix_fmt rgb24 - |
python3 -c '
import json, sys
meta = json.load(open(sys.argv[1]))
raw = sys.stdin.buffer.read()
px = [tuple(raw[i:i+3]) for i in range(0, len(raw), 3)]
dur = len(px) / 25
def rgb(h): return tuple(int(h[i:i+2], 16) for i in (1, 3, 5))
starts = []
pos = 0
for ch, m in zip(meta["chapters"], meta["markers"]):
    want = rgb(m)
    hit = next((i for i in range(pos, len(px)) if sum(abs(a - b) for a, b in zip(px[i], want)) < 90), None)
    if hit is None:
        sys.exit("marker not found for chapter: " + ch["title"])
    starts.append((hit / 25, ch["title"]))
    pos = hit + 25
starts[0] = (0.0, starts[0][1])
with open(sys.argv[2], "w") as f:
    f.write(";FFMETADATA1\ntitle=Null Drive: getting started\n")
    for i, (s, t) in enumerate(starts):
        e = starts[i + 1][0] if i + 1 < len(starts) else dur
        f.write(f"[CHAPTER]\nTIMEBASE=1/1000\nSTART={int(s*1000)}\nEND={int(e*1000)}\ntitle={t}\n")
with open(sys.argv[3], "w") as f:
    for s, t in starts:
        f.write(f"{int(s)//60}:{int(s)%60:02d} {t}\n")
print(open(sys.argv[3]).read(), end="")
' "$META" "$OUT/chapters.ffmeta" "$OUT/chapters.txt"

ffmpeg -v error -y -i "$IN" -i "$OUT/chapters.ffmeta" -map_metadata 1 -map_chapters 1 \
  -c:v libx264 -preset slow -crf 22 -pix_fmt yuv420p -movflags +faststart -an \
  "$OUT/null-drive-getting-started.mp4"
rm -f "$OUT/chapters.ffmeta"
echo "wrote $OUT/null-drive-getting-started.mp4 ($(du -h "$OUT/null-drive-getting-started.mp4" | cut -f1))"
