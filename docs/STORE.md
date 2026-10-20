# Publishing Null Drive in the Microsoft Store

The Store build is the same `NullDrive.exe` as the installer, packaged as an **MSIX** (`client/build/windows/msix/AppxManifest.xml`, built by `scripts/build-msix.sh`). The Store signs packages itself, so no code-signing certificate is needed for this route. That also means Store users don't get the SmartScreen warning the unsigned installer shows.

What the package declares, and why: [DESIGN.md › Microsoft Store](DESIGN.md#microsoft-store-msix).

## One-time setup

1. **Developer account.** [Partner Center](https://partner.microsoft.com/dashboard) › sign up as an individual developer (no registration fee for individuals since September 2025).
2. **Reserve the name.** Done: *Apps and games › Null Drive*, Store ID **9P3NQGJ8ZZMG**, listing https://apps.microsoft.com/detail/9P3NQGJ8ZZMG.
3. **The identity** (*Product management › Product identity*) is built into `scripts/build-msix.sh` as its defaults, so every build, local or CI, is ready to upload:

   | Partner Center field | Value | Override with |
   |---|---|---|
   | Package/Identity/Name | `NULLGRID.NullDrive` | `MSIX_IDENTITY_NAME` |
   | Package/Identity/Publisher | `CN=8685350B-5C4C-4422-9CDF-8AEB87F50B2F` | `MSIX_PUBLISHER` |
   | Package/Properties/PublisherDisplayName | `NULLGRID` | `MSIX_PUBLISHER_DISPLAY_NAME` |
   | Package/Properties/DisplayName | `Null Drive` (in the manifest) | |

   They aren't secrets: they end up in the public package. Package family name: `NULLGRID.NullDrive_yb898v9ybphdc`. The repository variables of the same names still override them in the release workflow, but aren't needed.

## Building the package

- **On every release tag:** the `store` job in `release.yml` builds `NullDrive_<version>_x64.msix` on a Windows runner (MakeAppx + MakePri from the Windows SDK) and uploads it as the **`store-package`** workflow artifact. It isn't attached to the GitHub release, because an unsigned MSIX doesn't install by double-clicking.
- **On every push:** the `msix` job in `ci.yml` builds a test-identity package, so a broken manifest fails CI.
- **By hand on Windows** (Git Bash, Windows SDK installed): `make windows && make msix`.
- **By hand on Linux/macOS:** build `makemsix` once (`git clone https://github.com/microsoft/msix-packaging && cd msix-packaging && ./makelinux.sh --pack`), then `MAKEMSIX=…/.vs/bin/makemsix make msix`. It validates the manifest against Microsoft's schemas, but doesn't build `resources.pri` (Windows then uses the 100% icons). Use the CI build for the Store.

The version is the git tag with a fourth part of `0` (`v0.8.0` → `0.8.0.0`); the Store requires that, and every submission needs a higher version than the last one.

**Version 5.0 is a Null Grid milestone.** From 5.0.0 on, the major version is shared by Null Search, Null Notes and Null Drive: any build at 5.0 or later has the cross-product sync and search features (Drive: per-user `/search`, `nulldrive://` links, the Notes backup, sync and E2EE APIs). Drive went from 0.8.3 straight to 5.0.0; tag the release `v5.0.0` so the exe, installer, MSIX (`5.0.0.0`), winget manifests and server all agree. `client/wails.json` and the two `package.json` files are kept at the same number.

## Submitting

In the product's submission:

1. **Packages:** upload the `.msix`. Device family: Desktop.
2. **Pricing and availability:** free; all markets (or your choice).
3. **Properties:** category *Productivity* (or *Utilities & tools*).
   - **Privacy policy URL** is required, because the app connects to the internet. The honest summary: Null Drive sends files only to the server the user configures and collects nothing for the developer. A page in the repo's GitHub Pages or README section works.
4. **Age ratings:** the questionnaire (no user-generated content shared with others, no ads): usually 3+ / Everyone.
5. **Store listing:** description (the README's first paragraph works), screenshots from `docs/screenshots/desktop-*.png` (PNG, 1960×1400: above the 1366×768 minimum; up to 10), feature list.
6. **Submission options › Notes for certification.** This matters for an app that needs a server:
   - **Restricted capability `runFullTrust`.** Say why: *"Null Drive is a desktop sync client (Win32 + WebView2) packaged as MSIX. Full trust is needed to sync a folder the user chooses, store the device token in Windows Credential Manager, and show a notification-area icon."*
   - **A server to test against.** Testers can't set up their own. Run a demo instance reachable over HTTPS, create a test account (`nulldrive add-user storetest`), and give the address, username and password in the notes, with the steps: *open the app › enter the address › Sign in › choose a folder*. Without one, testers can't get past the first screen, and the submission is likely to fail certification.

Certification usually takes a few business days. Updates: push a new tag, upload the new `store-package`, and submit.

## Testing a Store build on your own PC first (sideloading)

An MSIX only installs if it's signed by a certificate the PC trusts. For testing, use a self-signed one whose subject matches `MSIX_PUBLISHER`:

```powershell
# PowerShell, once: make a test certificate and trust it on this PC
$c = New-SelfSignedCertificate -Type Custom -Subject "CN=Null Drive Dev" -KeyUsage DigitalSignature `
     -FriendlyName "Null Drive test" -CertStoreLocation Cert:\CurrentUser\My `
     -TextExtension @("2.5.29.37={text}1.3.6.1.5.5.7.3.3", "2.5.29.19={text}")
$pw = ConvertTo-SecureString -String "test" -Force -AsPlainText
Export-PfxCertificate -Cert $c -FilePath test.pfx -Password $pw
Import-PfxCertificate -FilePath test.pfx -CertStoreLocation Cert:\LocalMachine\TrustedPeople -Password $pw   # admin prompt
```

```bash
# Git Bash: build and sign (osslsigncode, or signtool from the SDK)
MSIX_PUBLISHER="CN=Null Drive Dev" MSIX_CERT_PFX=test.pfx MSIX_CERT_PASSWORD=test make msix   # the test certificate's subject
```

Then double-click the `.msix` (or `Add-AppxPackage .\dist\NullDrive_*.msix`) and check:

- [ ] Start menu shows Null Drive with its icon; it opens and syncs.
- [ ] `start nulldrive://open?id=<an id from a search result>` (in `cmd`) opens the file.
- [ ] After the first launch it's listed in *Settings › Apps › Startup* (on), and starts in the tray at sign-in. The app's own "Start when I sign in" switch is replaced by a pointer to that page.
- [ ] *Settings › Open logs* opens a folder that has the log in it.
- [ ] Optional: run the Windows App Certification Kit (`appcert.exe test -appxpackagepath <msix> -reportoutputpath report.xml`, in the SDK) as the Store will.

Remove it with `Get-AppxPackage *NullDrive* | Remove-AppxPackage`.

## How the Store build differs from the installer

- **Data location.** Windows redirects the app's writes to `%AppData%` into a private per-package folder (`%LocalAppData%\Packages\<package family>\LocalCache\Roaming\Null Drive`). The app reads it transparently, but the installed and Store builds don't share settings. Uninstalling the Store app deletes that folder; the synced files and the token in Credential Manager stay (sign out in the app first to remove the token).
- **Starting at sign-in** is the manifest's startup task, managed in Windows Settings, not the app's switch.
- **Updates** come from the Store. Windows closes the app to update it; sync resumes on the next start, and interrupted transfers resume where they stopped.
- **Don't install both builds on one PC.** Both register `nulldrive://`, and both would sync if pointed at the same folder.
