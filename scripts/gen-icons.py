#!/usr/bin/env python3
"""Generate every Null Drive logo and icon file from one definition.

    python3 scripts/gen-icons.py            # writes all files listed below
    python3 scripts/gen-icons.py --preview out.png   # a contact sheet only

The mark is the "null" glyph (a circle crossed by a double slash, //) in a
drive bay, with a pink activity LED. Two variants:

  client  cyan slashes  - the desktop app (app icon, tray icons, its UI)
  server  red slashes   - the server and the web app it serves (favicon,
                          Docker / NAS app-store icons)

The SVG geometry here must match ui/src/lib/components/Logo.svelte (the same
32x32 coordinates); `make icons` regenerates the raster files. Needs Pillow.
"""
from __future__ import annotations

import argparse
import io
import os
import struct
import sys

from PIL import Image, ImageDraw, ImageFilter

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

# Neon Lab palette (ui/src/lib/styles/tokens.css, dark theme).
BASE = "#0a0a0f"
BAY = "#151a26"
BAY_EDGE = "#313b54"
CYAN = "#00e5ff"
PINK = "#ff2e88"
RED = "#ff2b45"  # --brand-server
SLASH = {"client": CYAN, "server": RED}

# Geometry in the 32x32 logo space (shared with Logo.svelte).
BAY_RECT = (2.5, 6.5, 29.5, 25.5)  # x0, y0, x1, y1, corner radius 3
CIRCLE = (16, 16, 5.5)
# Two parallel slashes, steeper than 45 degrees so they read as a typed "//".
SLASHES = [((10.75, 22), (17.25, 10)), ((14.75, 22), (21.25, 10))]
LED = ((5.5, 22.5), (7.5, 22.5))
STROKE = 2.0


def svg(variant: str, standalone_tile: bool = False) -> str:
    """The mark as SVG. standalone_tile adds the rounded dark app-icon tile."""
    s = SLASH[variant]
    (a0, a1), (b0, b1) = SLASHES
    body = (
        f'<rect x="2.5" y="6.5" width="27" height="19" rx="3" fill="{BAY}" stroke="{BAY_EDGE}" stroke-width="1.5"/>'
        f'<circle cx="16" cy="16" r="5.5" fill="none" stroke="{CYAN}" stroke-width="2"/>'
        f'<path d="M{a0[0]} {a0[1]} {a1[0]} {a1[1]}M{b0[0]} {b0[1]} {b1[0]} {b1[1]}" '
        f'stroke="{s}" stroke-width="2" stroke-linecap="round"/>'
        f'<path d="M5.5 22.5h2" stroke="{PINK}" stroke-width="2" stroke-linecap="round"/>'
    )
    if standalone_tile:
        # 256 tile with the 32-unit mark scaled into the middle.
        return (
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">'
            f'<rect width="256" height="256" rx="56" fill="{BASE}"/>'
            f'<g transform="translate(16 16) scale(7)">{body}</g></svg>\n'
        )
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">{body}</svg>\n'


def _rgba(hex_: str, a: int = 255) -> tuple[int, int, int, int]:
    h = hex_.lstrip("#")
    return (int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16), a)


def _line(d: ImageDraw.ImageDraw, p0, p1, width: float, color, k: float) -> None:
    w = max(1, round(width * k))
    a = (p0[0] * k, p0[1] * k)
    b = (p1[0] * k, p1[1] * k)
    d.line([a, b], fill=color, width=w)
    r = w / 2
    for x, y in (a, b):  # round caps
        d.ellipse([x - r, y - r, x + r, y + r], fill=color)


def raster(variant: str, size: int, mode: str = "tile", slash_color: str | None = None,
           glow: bool = True, backdrop: bool = True) -> Image.Image:
    """Render at `size` px with 8x supersampling.

    mode "tile":  the full mark (bay, null glyph, LED) on the rounded dark
                  app-icon tile. Used from 48 px up.
    mode "glyph": just the null glyph, filling a small dark rounded square.
                  The bay and LED turn to mush below ~32 px, so small icon
                  sizes and the tray icons use this simplified form."""
    ss = 8
    big = size * ss
    img = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    if backdrop:
        d.rounded_rectangle([0, 0, big - 1, big - 1], radius=big * (56 / 256 if mode == "tile" else 0.22),
                            fill=_rgba(BASE))
    if mode == "tile":
        k = big * 7 / 256  # 32-unit mark at 7x inside a 256 tile, 16 px margin
        o = 16 / 7
    else:
        k = big / 15.0  # the glyph's 13-unit box plus a 1-unit margin each side
        o = -8.5

    def tx(p):
        return (p[0] + o, p[1] + o)

    if mode == "tile":
        x0, y0, x1, y1 = BAY_RECT
        p0, p1 = tx((x0, y0)), tx((x1, y1))
        d.rounded_rectangle([p0[0] * k, p0[1] * k, p1[0] * k, p1[1] * k], radius=3 * k,
                            fill=_rgba(BAY), outline=_rgba(BAY_EDGE), width=max(1, round(1.5 * k)))
    cx, cy, r = CIRCLE
    c = tx((cx, cy))
    ring = Image.new("RGBA", img.size, (0, 0, 0, 0))
    rd = ImageDraw.Draw(ring)
    rd.ellipse([(c[0] - r) * k, (c[1] - r) * k, (c[0] + r) * k, (c[1] + r) * k],
               outline=_rgba(CYAN), width=max(1, round(STROKE * k)))
    color = _rgba(slash_color or SLASH[variant])
    for a, b in SLASHES:
        _line(rd, tx(a), tx(b), STROKE, color, k)
    if glow and size >= 48:
        img.alpha_composite(ring.filter(ImageFilter.GaussianBlur(radius=1.2 * k)))  # neon bloom
    img.alpha_composite(ring)
    if mode == "tile":
        _line(d, tx(LED[0]), tx(LED[1]), STROKE, _rgba(PINK), k)
    return img.resize((size, size), Image.LANCZOS)


def icon(variant: str, size: int, **kw) -> Image.Image:
    """App icon at one size: the full mark from 48 px, the glyph below."""
    return raster(variant, size, "tile" if size >= 48 else "glyph", **kw)


def write_ico(path: str, images: list[Image.Image]) -> None:
    """Multi-size .ico with PNG-compressed entries (Windows Vista+)."""
    entries = []
    for im in images:
        buf = io.BytesIO()
        im.save(buf, "PNG")
        entries.append((im.size[0], buf.getvalue()))
    header = struct.pack("<HHH", 0, 1, len(entries))
    offset = 6 + 16 * len(entries)
    dir_, data = b"", b""
    for size, png in entries:
        dim = 0 if size >= 256 else size
        dir_ += struct.pack("<BBBBHHII", dim, dim, 0, 0, 1, 32, len(png), offset + len(data))
        data += png
    with open(path, "wb") as f:
        f.write(header + dir_ + data)


def installer_panel() -> Image.Image:
    """Left panel of the installer's welcome and finish pages: the mark and
    the wordmark on a dark neon gradient. 24-bit BMP (NSIS takes no alpha)."""
    from PIL import ImageFont

    w, h = 164, 314
    img = Image.new("RGBA", (w, h), _rgba(BASE))
    px = img.load()
    for y in range(h):  # faint cyan glow from the top, pink from the bottom
        t, b = max(0.0, 1 - y / 170), max(0.0, 1 - (h - y) / 150)
        for x in range(w):
            px[x, y] = (int(10 + 30 * b), int(10 + 22 * t), int(15 + 30 * t + 18 * b), 255)
    # The mark without its app-icon tile: bay straight on the gradient.
    mark = raster("client", 168, "tile", backdrop=False).crop((0, 30, 168, 138))  # the bay
    img.alpha_composite(mark, ((w - 168) // 2, 66))
    d = ImageDraw.Draw(img)
    font = None
    for f in ("DejaVuSansMono-Bold.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"):
        try:
            font = ImageFont.truetype(f, 15)
            break
        except OSError:
            pass
    if font:
        gap = 7
        a, b = d.textlength("NULL", font=font), d.textlength("DRIVE", font=font)
        x = (w - (a + gap + b)) / 2
        d.text((x, 196), "NULL", fill=_rgba(CYAN), font=font, anchor="lm")
        d.text((x + a + gap, 196), "DRIVE", fill=(215, 222, 235, 255), font=font, anchor="lm")
    d.line([(28, 220), (w - 28, 220)], fill=_rgba(BAY_EDGE), width=1)
    return img.convert("RGB")


def wide_tile(w: int, h: int) -> Image.Image:
    """Wide Start tile: the mark centered on the dark base."""
    img = Image.new("RGBA", (w, h), _rgba(BASE))
    m = raster("client", int(h * 0.8), "tile", backdrop=False)
    img.alpha_composite(m, ((w - m.width) // 2, (h - m.height) // 2))
    return img


def installer_header() -> Image.Image:
    w, h = 150, 57
    img = Image.new("RGB", (w, h), (255, 255, 255))  # MUI header pages are light
    mark = raster("client", 44, "tile", glow=False)
    img.paste(mark, (w - 44 - 8, (h - 44) // 2), mark)
    return img


TRAY = {  # tray icon per sync state: the slash color carries the state
    "idle": CYAN,
    "syncing": PINK,
    "paused": "#8d9ab0",
    "offline": "#ffb020",
    "error": "#ff4d5e",
}

ICO_SIZES = [16, 20, 24, 32, 40, 48, 64, 128, 256]


def generate() -> list[str]:
    out = []

    def save(img: Image.Image, rel: str):
        p = os.path.join(ROOT, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        img.save(p, optimize=True)
        out.append(rel)

    def text(rel: str, s: str):
        p = os.path.join(ROOT, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w") as f:
            f.write(s)
        out.append(rel)

    # Brand sources.
    for v in ("client", "server"):
        text(f"docs/brand/nulldrive-{v}.svg", svg(v))
        text(f"docs/brand/nulldrive-{v}-tile.svg", svg(v, standalone_tile=True))
        save(icon(v, 512), f"docs/brand/nulldrive-{v}-512.png")

    # Desktop app (client): Wails app icon, Windows .ico, installer artwork.
    save(icon("client", 1024), "client/build/appicon.png")
    ico = os.path.join(ROOT, "client/build/windows/icon.ico")
    os.makedirs(os.path.dirname(ico), exist_ok=True)
    write_ico(ico, [icon("client", s) for s in ICO_SIZES])
    out.append("client/build/windows/icon.ico")

    # Installer artwork (NSIS: 164x314 welcome/finish panel, 150x57 header).
    save(installer_panel(), "client/build/windows/installer/welcome.bmp")
    save(installer_header(), "client/build/windows/installer/header.bmp")

    # Microsoft Store (MSIX) visual assets. The unqualified file is the
    # 100% size (used as-is when the package has no resources.pri, e.g. a
    # Linux test build); the scale-/targetsize- variants are picked by
    # Windows when MakePri indexes them (the Store build on Windows CI).
    msix = "client/build/windows/msix/Assets"
    for name, base in (("Square44x44Logo", 44), ("Square150x150Logo", 150), ("StoreLogo", 50)):
        save(icon("client", base), f"{msix}/{name}.png")
        for scale in (200, 400):
            save(icon("client", base * scale // 100), f"{msix}/{name}.scale-{scale}.png")
    for t in (16, 24, 32, 48, 256):  # taskbar, Start, Alt+Tab, Settings
        save(icon("client", t), f"{msix}/Square44x44Logo.targetsize-{t}.png")
        save(icon("client", t), f"{msix}/Square44x44Logo.targetsize-{t}_altform-unplated.png")
    for scale, (w, h) in ((None, (310, 150)), (200, (620, 300))):
        save(wide_tile(w, h), f"{msix}/Wide310x150Logo{'' if scale is None else f'.scale-{scale}'}.png")

    # Tray icons: bay-only crop, state color on the slashes.
    for state, color in TRAY.items():
        imgs = [raster("client", s, "glyph", slash_color=color, glow=False) for s in (16, 20, 24, 32, 48)]
        p = os.path.join(ROOT, f"client/trayicons/{state}.ico")
        write_ico(p, imgs)
        out.append(f"client/trayicons/{state}.ico")
        save(raster("client", 64, "glyph", slash_color=color, glow=False), f"client/trayicons/{state}.png")

    # Server + web app: favicon, Docker / NAS app-store icons.
    text("ui/public/favicon.svg", svg("server"))
    save(icon("server", 180), "ui/public/apple-touch-icon.png")
    text("deploy/assets/icon.svg", svg("server", standalone_tile=True))
    save(icon("server", 512), "deploy/assets/icon.png")
    save(icon("server", 512), "deploy/casaos/Apps/NullDrive/icon.png")
    return out


def preview(path: str) -> None:
    W, H = 1240, 470
    sheet = Image.new("RGBA", (W, H), _rgba("#1b1f2a"))
    x = 30
    for v in ("client", "server"):
        sheet.alpha_composite(icon(v, 256), (x, 30))
        xx = x
        for s in (64, 48, 32, 24, 16):
            sheet.alpha_composite(icon(v, s), (xx, 310))
            xx += s + 14
        x += 300
    x = 650
    for state, color in TRAY.items():
        im = raster("client", 32, "glyph", slash_color=color, glow=False)
        sheet.alpha_composite(im.resize((96, 96), Image.NEAREST), (x, 30))
        sheet.alpha_composite(raster("client", 16, "glyph", slash_color=color, glow=False), (x, 140))
        sheet.alpha_composite(raster("client", 32, "glyph", slash_color=color, glow=False), (x + 30, 140))
        x += 112
    sheet.convert("RGB").save(path)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--preview")
    a = ap.parse_args()
    if a.preview:
        preview(a.preview)
        sys.exit(0)
    for f in generate():
        print("wrote", f)
