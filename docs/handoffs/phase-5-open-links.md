# HANDOFF: Open links (Phase 5): nulldrive://, Windows install, Microsoft Store

**Status:** done for Null Drive. A Null Search result's `nulldrive://open?id=<id>` link now opens the file in the desktop app. If a copy is already running, the link is handed to it in ~250 ms without starting a second UI. The Windows installer registers the scheme per user and removes it on uninstall. The portable exe registers itself if nothing else owns the scheme. There's a **Microsoft Store (MSIX) package** with the protocol and start-at-sign-in declared in its manifest, a build script, CI jobs and a Partner Center guide.
**Not in this conversation:** `nullnotes://` belongs to the Null Notes app (its own codebase and conversation). The rules both apps follow are in `docs/OPEN-LINKS.md` and in the Project as `null-grid/uri-scheme-contract.md`.
**Previous handoffs:** cross-product search Phase 3 in `docs/handoffs/search-phase-3.md`; Windows installer + logos in `docs/handoffs/phase-8-part1.md`.

## First thing to do (owner)

```bash
go mod tidy && (cd client && go mod tidy)
make test && make lint && make e2e && make smoke
make windows                      # installer + portable zip (now registering nulldrive://)
make wine-test                    # optional: installer + links under Wine (needs wine, xvfb-run)
```

To publish in the Store, follow **`docs/STORE.md`**:
1. Reserve the name in Partner Center (free for individual developers).
2. Put the three identity values in repository variables.
3. Tag a release and download the `store-package` artifact (the `.msix`).
4. Upload it, with a demo server and account in the certification notes.

Before submitting, test a sideloaded build on a real Windows PC (the checklist is in STORE.md). Wine can't run MSIX packages or WebView2, so that part is unverified here.

## What was built and where

| Path | What |
|---|---|
| `client/link.go` | Strict link parser (`open` + 32-hex id only), `--url` command-line handling (a link makes every other flag ignored), the dangerous-file-type list |
| `client/openlink.go` | Resolve the id with the server → path in the sync folder → open, or **reveal** for programs/scripts/shortcuts/symlinks; sync-and-wait (20 s) for files not downloaded yet; native dialogs for not set up / locked / not found / deleted / not synced / unreachable, offering the web app |
| `client/ipc_windows.go` | Hands args to a running copy without starting Wails (generalizes Phase 8's `--quit`): mutex check, window lookup with a short retry, `SendMessageTimeout` (a hung copy can't hang the launcher), `AllowSetForegroundWindow` so the opened file comes to the front |
| `client/shell_windows.go` | `ShellExecute` / `SHOpenFolderAndSelectItems` (not `explorer.exe <path>`, which mis-parses commas), `AssocIsDangerous`, MSIX detection, real paths for "Open logs" inside a package, self-registration for the portable copy |
| `client/main.go`, `app.go` | `--url`, start hidden for links, `OnDomReady` handling, macOS `OnUrlOpen`; Store build hides the start-at-login switch (Windows owns it there) |
| `client/build/windows/installer/nulldrive.nsi` | `HKCU\Software\Classes\nulldrive` → `"…\NullDrive.exe" --url "%1"`, shell notified; uninstall removes it only if it still points at this install |
| `client/build/windows/msix/AppxManifest.xml`, `Assets/` | Store manifest: full trust, `uap3:Protocol` with `Parameters="--url "%1""`, `windows.startupTask` (`--hidden`), 21 icons (scales, target sizes, unplated) from `make icons` |
| `scripts/build-msix.sh` (`make msix`) | Stages and packs: MakePri + MakeAppx on Windows (CI, the Store build), `makemsix` elsewhere; optional test signing (`MSIX_CERT_PFX`, osslsigncode) |
| `.github/workflows/ci.yml` › `msix`, `release.yml` › `store` | Windows-runner MSIX builds; release identity from repository variables; `store-package` artifact (not attached to the release) |
| `client/wails.json`, `client/build/linux/nulldrive.desktop` | Scheme declarations for future macOS / Linux packages |
| Server: `GET /api/v2/ids/{id}` | Stable id → current file (deleted ones flagged); other users' ids are 404; search-only tokens refused |
| Web app: `/open?id=` | Resolves the id after sign-in, shows the file highlighted in its folder (`?show=`), explains deleted / not in this account |
| Docs | `docs/STORE.md` (new), `docs/OPEN-LINKS.md` (new, shared with Notes), DESIGN.md › Opening links, SECURITY.md › 7. A malicious web page, API.md, INSTALL.md, client README, README |

## How it was tested

- **Go, client:** the parser rejects 19 hostile forms, including quote breakout, path traversal, duplicate ids and over-long input. Command-line handling covers the crafted `--url <link> --forget y` case. Dangerous-type list; resolve flow against a temp folder (open, reveal for `.exe`, executable bit and symlinks, folders, deleted, not synced, path escape refused, sync-then-open, E2EE name decryption, unreachable, not set up, locked); registry command format. Vet for Windows, macOS and Linux; tests under `-race`.
- **Go, server:** `TestFileByID` covers moves, other users, missing, bad ids, deleted, and search tokens. Full suite under `-race`; golangci-lint 0 issues; the client's only lint findings are the 9 pre-existing gosec ones.
- **Web:** Vitest 132 (+4 for routes and the `/open` page), svelte-check 0/0; Playwright e2e 8/8, which now follows a real `/open?id=` link to the highlighted file and checks an unknown id.
- **Windows, under Wine 9** (`scripts/wine-test.sh`, `make wine-test`, with stand-ins in `client/internal/winetest/`): 13/13 on a fresh Wine prefix with the delivered installer.
  - The installer writes the four registry values, and the uninstaller removes them. It leaves another copy's registration alone, and a reinstall takes the scheme back.
  - The **real `NullDrive.exe`** forwards a link to a running copy (a stand-in with Wails' mutex and message window) and exits. It sends only `--url <link>` even when launched with `--forget … --quit`: 30 runs out of 30, 259 ms on average including Wine's process start.
  - Two robustness fixes came out of this. A hung running copy can no longer hang the link launcher or the installer's `--quit` (`SendMessageTimeout`, 5 s). If Wails' mutex shows a copy is running but its window isn't found yet, the lookup retries for 2 s.
  - A link fired through `ShellExecute` (as browsers do) launches exactly `NullDrive.exe --url <link>`. This needed the key mirrored machine-wide, because Wine 9 doesn't merge per-user classes into `HKEY_CLASSES_ROOT` the way Windows does.
  - A link crafted to break out of the quotes really does produce extra arguments, and the app ignores them (unit-tested with the exact array).
- **MSIX:** packed with Microsoft's `makemsix` (built from source with schema validation) and unpacked/verified. Three deliberately broken manifests were rejected, so the validator does check. It was test-signed with a self-signed `CN=Null Drive Dev` certificate via osslsigncode and verified (`Signature verification: ok`). The delivered package is unsigned, as the Store wants.

## Known issues / next steps

- **Not run on real Windows:** the app's dialogs and file opening (WebView2 doesn't run under Wine), the MSIX install, protocol activation and startup task. The STORE.md sideload checklist covers them. The CI `msix` job builds with the real MakeAppx/MakePri on its first push.
- **Store certification** needs a demo server and test account for Microsoft's testers (STORE.md › Notes for certification) and a privacy policy URL.
- The installed and Store builds shouldn't both be on one PC (both claim `nulldrive://`, separate settings).
- Carried over: code signing for the installer, winget submission, Linux/macOS packages, `nulldrive-e2ee` in releases.

## Draft resume bullets

- Implemented a hardened custom URI scheme (`nulldrive://`) for a Go/Wails desktop sync client. Links are treated as untrusted input: a strict parser, server-side id resolution, argument-injection immunity, and executables revealed rather than launched. Links are forwarded to the running instance over a Win32 `WM_COPYDATA` channel in ~250 ms. Verified with a Wine-based end-to-end harness that fires real shell launches.
- Packaged the app for the Microsoft Store as MSIX: full-trust manifest with protocol and startup-task extensions, scaled icon sets, and CI on Windows runners with MakeAppx/MakePri. The manifest is schema-validated with Microsoft's open-source packer and test-signed for sideloading, alongside the existing per-user NSIS installer.
