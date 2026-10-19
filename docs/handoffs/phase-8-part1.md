# HANDOFF — Phase 8 (in progress): Windows app + logos

**Status:** Phase 8, part 1 done. The desktop app now ships as a **real Windows installer** (`NullDrive-<v>-Setup.exe`), a portable zip and winget manifests, built by one script from any OS and by CI on every push. The logos are final: one mark, a double slash `//`, **cyan for the desktop app, red for the server and web app**, all generated from one script.
**Still to do in Phase 8:** Microsoft Store (MSIX + Partner Center), winget submission, code-signing certificate, Linux `.deb`/AppImage and macOS `.dmg`, auto-update, shipping `nulldrive-e2ee` in releases, README/Pages/interview kit polish.
**Phase 7's handoff** is in `docs/handoffs/phase-7.md` (and the Project as `null-drive/phase-7-HANDOFF.md`).

## First thing to do (owner)

This time the sandbox could fetch most modules straight from GitHub, so the **desktop app was built against the real Wails v2.10.2, systray, go-keyring and fsnotify**. Two stand-ins remain, only for local builds: `golang.org/x/*` from their GitHub mirrors, and the ncruces pure-Go SQLite driver in place of `modernc.org/sqlite` (same DSN). `client/go.mod` now requires `golang.org/x/sys` directly (for `--quit`). Then:

```bash
go mod tidy && (cd client && go mod tidy)       # writes the go.sum files
make test && make lint && make e2e && make smoke
sudo apt install nsis                            # or: brew install makensis / choco install nsis
make windows                                     # → dist/NullDrive-<v>-Setup.exe, portable zip, winget/
```

Push to see the new `windows-installer` CI job build the installer (artifact `NullDrive-windows`), next to the native Windows `desktop` job.

## What was built and where

| Path | What |
|---|---|
| `scripts/gen-icons.py` (`make icons`) | **Every logo and icon from one definition**: SVG + PNG brand marks (`docs/brand/`), app icon (`client/build/appicon.png`), multi-size Windows `.ico` (16–256), tray icons per sync state, installer artwork, favicon, apple-touch icon, Docker/CasaOS icons. Small sizes (<48 px) and tray icons use the glyph without the bay so `//` stays legible |
| `ui/src/lib/components/Logo.svelte` | Double-slash mark; `variant="client"/"server"` or the `--logo-slash` CSS variable. The web app sets red in `ui/src/web/app.css`; the desktop app keeps cyan |
| `ui/src/lib/styles/tokens.css` | `--brand-server` (+ `-line`), a brand red kept separate from `--danger` |
| `scripts/build-windows.sh` (`make windows`) | Frontend → Windows resources (`go-winres`: icon, manifest, version info) → `NullDrive.exe` cross-compiled (`-tags desktop,production -H windowsgui`) → portable zip → NSIS installer → winget manifests. Optional signing with `osslsigncode` when `WINDOWS_CERT_PFX` is set |
| `client/build/windows/winres.json` | Manifest: per-monitor-v2 DPI, Common Controls v6, as invoker, long paths; version info from the tag |
| `client/build/windows/installer/nulldrive.nsi` | Per-user installer (no admin) into `%LOCALAPPDATA%\Programs\Null Drive`: Start menu, Apps & features entry, clean upgrade, WebView2 check, uninstaller that never touches the sync folder and asks before removing settings |
| `client/main.go`, `quit_windows.go`, `quit_other.go`, `config.go` | `NullDrive --quit` (tells a running copy to quit through Wails' single-instance window, **without starting Wails**, so a missing WebView2 can't hang the installer) and `NullDrive --forget` (removes settings + keychain entries) |
| `client/lifecycle_test.go` | `--forget` removes config, token and encryption key, never the synced folder |
| `scripts/winget-manifest.py` | winget manifests (schema 1.6: nullsoft, user scope, `/S`, WebView2 dependency) |
| `scripts/desktop-screenshots.mjs` | Regenerates `docs/screenshots/desktop-*.png` from the desktop UI's browser preview (was a manual step before) |
| `.github/workflows/ci.yml` | `desktop` (Windows): vet + **tests** + native build; new `windows-installer` (Linux): full installer build |
| `.github/workflows/release.yml` | Desktop job now builds on Linux with the script; release attaches the installer, portable zip, winget manifests; optional signing secrets `WINDOWS_CERT_PFX_BASE64` + `WINDOWS_CERT_PASSWORD` |
| Docs | `docs/DESIGN.md` › Phase 8 (logos, why cross-compile, per-user NSIS, `--quit` IPC, WebView2, signing, winget); `docs/INSTALL.md` › Desktop app (Windows); `client/README.md` › Windows build and installer; README install line, logos, installer screenshot |
| `docs/screenshots/`, `docs/video/` | All screenshots regenerated with the new logos (web + desktop), plus `windows-installer.png`; tutorial video re-recorded (2:56) |

Also fixed: last round's zip had the Phase 6 `HANDOFF.md`, no root `SECURITY.md`, and a stray 6.7 MB stub binary at `client/client` (now ignored by `.gitignore`).

## How it was tested

- **Real build:** `NullDrive.exe` is a PE32+ GUI binary built from the real Wails; its resources (icon, manifest, version 0.7.0.0) were extracted and checked.
- **Under Wine 9 (64- and 32-bit) with a virtual display:** GUI install through every page (screenshots in `docs/screenshots/windows-installer.png`), the WebView2-missing prompt, silent install, upgrade over an existing install, silent uninstall (files, shortcut, Apps & features entry and autostart key removed; settings kept, as designed), `--forget` and `--quit` returning at once without the WebView2 prompt, and `--quit` delivered to a stand-in that creates Wails' message window and decodes the message exactly as Wails does. The app window itself needs WebView2, which Wine can't run; it stops at Wails' "WebView2 runtime is required" prompt, which proves the binary starts.
- **Everything else re-run:** Go tests (all packages), golangci-lint (0 issues), client tests incl. `GOOS=windows go vet` with the real deps, Vitest 128 + 18, svelte-check both apps, Playwright e2e 8/8 and the screenshot suite 7/7, smoke test, actionlint on all workflows.

## Known issues / shortcuts

- **Not code-signed:** SmartScreen will warn until a certificate is added (DESIGN.md › Code signing lists the options). The hook takes a `.pfx`; Azure Trusted Signing would replace that step with Microsoft's action.
- **Not yet on winget or the Store.** Manifests are generated per release; the PR to `microsoft/winget-pkgs` and the MSIX/Partner Center work are next.
- **x64 only.** An arm64 `NullDrive.exe` is one `GOARCH` away; the installer is x64-only for now.
- The app's own UI wasn't rendered on Windows here (no WebView2 under Wine); the native `desktop` CI job builds and tests it on Windows.
- Carried over from Phase 7: CI security scans and CodeQL run for the first time after push; pre-existing gosec warnings in `client/platform.go`/`app.go`.

## What the rest of Phase 8 needs to know

- Version flows from the git tag: `scripts/build-windows.sh v0.8.0` sets the exe and installer version info, the installer name and the winget manifests.
- `NullDrive --quit` / `--forget` are part of the installer contract: keep them working when touching `main.go`. The single-instance ID `io.nulldrive.client` must not change (the `--quit` IPC derives Wails' window names from it).
- Store submission needs an MSIX (MakeAppx on Windows, or `wails` MSIX tooling); its identity/publisher come from Partner Center.
- `nulldrive-e2ee` still isn't in `scripts/build-release.sh`.

## Draft resume bullets

- Shipped a cross-compiled Windows desktop installer for a Go/Wails sync client: one script builds the `.exe` (icon, DPI-aware manifest, version info, optional code signing), a per-user NSIS installer and winget manifests from Linux in CI, with a single-instance IPC so upgrades and uninstalls stop the running app cleanly instead of killing it mid-sync.
- Designed the product's two-variant logo system (client cyan / server red) generated from a single source into every format the apps need (SVG, multi-size ICO, tray states, installer art), keeping web, desktop, Docker and store assets in sync.
