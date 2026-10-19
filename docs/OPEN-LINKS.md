# Null franchise: open links (nulldrive:// and nullnotes://), v1

Companion to the search contract (`null-grid/search-contract-v1.md`): every search result's `open` field is a link the owning desktop app handles. This doc is the shared rule set for both apps. Null Drive implements `nulldrive://` (Phase 5, see `null-grid/null-drive-phase5-open-links.md`). Null Notes implements `nullnotes://` in its own codebase, following the same rules.

## Link format

```
nulldrive://open?id=<file id>     32 lowercase hex characters (Drive's stable file id)
nullnotes://open?id=<note id>     a UUID, lowercase, 8-4-4-4-12 (the note's frontmatter id)
```

- One action: `open`. Accept `scheme://open?id=…`, `scheme://open/?id=…` and `scheme:open?id=…`; scheme and action case-insensitive; the id may arrive in upper case (normalize to lower).
- Reject everything else: other actions, paths after the action, user info, fragments, a missing or repeated `id`, an id that doesn't match the format exactly, links over 2048 characters.
- Null Search renders `open` as a plain link (`<a href="nulldrive://open?id=…">`). The browser asks the user before launching the app.

## Handler rules (both apps)

A URL scheme can be launched by any web page, so the link is untrusted input.

1. **Command line.** Register `"<app>.exe" --url "%1"`. When the arguments contain a link (after `--url`, or a bare argument starting with `<scheme>:`), ignore every other flag. A crafted link that breaks out of the quotes must not be able to trigger maintenance flags (`--forget`, `--quit`, and the like).
2. **Never take a path from the link.** Look the id up in the app's own data: Drive asks its server (`GET /api/v2/ids/{id}`, for the account this computer syncs); Notes looks it up in its vault index (frontmatter `id:`, `.nullnotes/ids.json` for non-Markdown notes).
3. **Never run code because of a link.** Drive opens documents with their default app but only *reveals* programs, scripts, installers, shortcuts, types Windows' `AssocIsDangerous` flags, executable-bit files and symlinks. Notes opens notes in its own editor, so this mostly doesn't arise, but attachments follow the Drive rule.
4. **Locked notes** (`*.lock`, `NNLOCK1\n`): open the unlock prompt, never the content.
5. **Single instance.** If the app is running, the new process hands the link to it and exits (on Windows without starting the UI framework: Drive uses Wails' single-instance window and `WM_COPYDATA`, after `AllowSetForegroundWindow` so the opened item comes to the front). If it isn't running, start, then open the link once the window is ready.
6. **One link at a time.** Ignore links that arrive while one is being opened.
7. **Say why when it can't open.** Cases: not set up, locked, not found (or another account's), deleted, not synced yet (Drive syncs and waits up to 20 s first), unreachable. Drive offers its web app's `/open?id=` page as a fallback.

## Registration

| Build | How | Notes |
|---|---|---|
| Windows installer | `HKCU\Software\Classes\<scheme>`: `(Default)="URL:<App>"`, `URL Protocol=""`, `DefaultIcon`, `shell\open\command="\"<exe>\" --url \"%1\""`, then `SHChangeNotify(SHCNE_ASSOCCHANGED)` | per user, no admin; the uninstaller removes it only if the command still points at its own exe |
| Portable exe | register at start only if no other existing copy owns the scheme | so trying a portable copy doesn't take links from the installed one |
| Microsoft Store (MSIX) | `<uap3:Extension Category="windows.protocol"><uap3:Protocol Name="<scheme>" Parameters="--url &quot;%1&quot;">` | HKCU writes inside a package are private to it, so the manifest is the only way; same for start-at-login (`desktop:StartupTask`, not the Run key) |
| macOS | `CFBundleURLTypes` (Wails: `wails.json` › `info.protocols`); links arrive via `OnUrlOpen` | |
| Linux | `.desktop` file with `MimeType=x-scheme-handler/<scheme>` and `Exec=<app> --url %u` | |

## Test checklist (each app)

- Parser rejects the hostile forms above, including `…?id=<id>" --forget "x`.
- Under Windows (or Wine): installer writes the keys; firing the link through `ShellExecute` (as browsers do) launches `<exe> --url <link>`; a crafted link's extra arguments are ignored; uninstall removes only its own registration.
- A running copy receives the link from a second launch, and the second process exits at once.
- MSIX manifest validates (MakeAppx, or `makemsix pack` with the validation parser).
