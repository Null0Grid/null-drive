# GitHub setup checklist

The repository is [Null0Grid/null-drive](https://github.com/Null0Grid/null-drive). These one-time settings make CI, Dependabot and releases work. About 10 minutes.

## 1. Repository settings

- [ ] **Settings → General → Default branch:** `main`.
- [ ] **Settings → Actions → General → Actions permissions:** "Allow all actions and reusable workflows", or an allow-list: `actions/*`, `docker/*`, `github/codeql-action/*`, `golangci/golangci-lint-action`, `hadolint/hadolint-action`, `softprops/action-gh-release`.
- [ ] **Settings → Actions → General → Workflow permissions:** keep the default, "Read repository contents and packages permissions". Each job asks for what it needs: `packages: write` for the image, `contents: write` to attach release files.
- [ ] **Settings → Code security:** turn on *Dependency graph*, *Dependabot alerts* and *Dependabot security updates*. Version updates are configured in `.github/dependabot.yml`.
- [ ] **Microsoft Store identity** (when the app is reserved in Partner Center): **Settings → Secrets and variables → Actions → Variables**: `MSIX_IDENTITY_NAME`, `MSIX_PUBLISHER`, `MSIX_PUBLISHER_DISPLAY_NAME`, from Partner Center → Product management → Product identity. Until then the `store-package` MSIX has placeholder identity values. See [STORE.md](STORE.md).
- [ ] *(Optional)* Code signing for the Windows installer: repository secrets `WINDOWS_CERT_PFX_BASE64` (base64 of a `.pfx`) and `WINDOWS_CERT_PASSWORD`. Without them the installer is unsigned and SmartScreen asks once.
- [ ] *(Recommended)* **Settings → Rules → Rulesets** for `main`: require a pull request and the CI checks.

## 2. A release

- [ ] CI is green on `main` (Actions tab).
- [ ] The version is the same in `client/wails.json` (`info.productVersion`), `ui/package.json` and `client/frontend/package.json`. CI checks this, and the release refuses a tag that doesn't match.
- [ ] Create the release: **Releases → Draft a new release → tag `5.0.0` (new tag) → target `main` → Publish**. Or push a tag: `git tag 5.0.0 && git push origin 5.0.0`. A `v` prefix works too.
- [ ] Watch **Actions → Release**: version check → CI and security on the tag → image, server packages, desktop app → files attached to the release; the Store package is a separate artifact of the run.
- [ ] For a tag that already exists (or a failed run): **Actions → Release → Run workflow**, and type the tag.
- [ ] **Make the container package public** (new GHCR packages start private): the org's **Packages** → `null-drive` → **Package settings** → *Change visibility* → **Public**.
- [ ] Check it:
  ```bash
  docker run --rm ghcr.io/null0grid/null-drive:5.0.0 version   # prints v5.0.0 and the commit
  ```
  The release should have 6 server archives, 2 `.deb`, 2 Docker archives, the installer, the portable zip, 3 winget manifests and `SHA256SUMS.txt`.

## If something fails

| Failure | Likely cause |
|---|---|
| `tag 5.0.1 but client/wails.json says 5.0.0` | Bump the three version fields, commit, and tag that commit. |
| `denied: permission_denied` / `403` pushing to ghcr.io | Workflow permissions were tightened below the default, or the package belongs to another repo: Package settings → *Manage Actions access* → add this repo with **Write**. |
| `resource not accessible by integration` when attaching files | `contents: write` is blocked at the organization level: allow it for workflows in the org settings. |
| `go mod tidy -diff` fails in CI | A dependency changed without `go mod tidy`: run it in the root and in `client/`, and commit `go.mod`/`go.sum`. |
