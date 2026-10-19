# GitHub setup checklist

One-time steps so CI, Dependabot and the release pipeline work. ~15 minutes.

## 1. Prepare the code (locally)

- [ ] Create a **public** GitHub repo named `nulldrive`, **empty** (no README, license or .gitignore: they're already here).
- [ ] In this folder, run once (`you` = your GitHub username; Go 1.24+ needed):
  ```bash
  scripts/prepare-github.sh you "Your Name"
  ```
  It replaces every placeholder (`OWNER` in image names, links and NAS templates; the Go module path; the LICENSE name), generates `go.sum` for both Go modules (`go mod tidy`; CI and the Docker build need it), runs the Go tests, commits, and tags `v5.0.0`. Doing it by hand instead: the same `OWNER` → `you` and `github.com/nulldrive/nulldrive` → `github.com/you/nulldrive` replacements, then `go mod tidy` in the root and in `client/`.
- [ ] Push:
  ```bash
  git remote add origin https://github.com/you/nulldrive.git
  git push -u origin main
  ```
  Push the tag only after step 2 (it starts the release).

## 2. Repository settings

- [ ] **Settings → Actions → General → Actions permissions:** "Allow all actions and reusable workflows" (or allow-list: `actions/*`, `docker/*`, `golangci/golangci-lint-action`, `hadolint/hadolint-action`).
- [ ] **Settings → Actions → General → Workflow permissions:** leave **"Read repository contents and packages permissions"** (the default). Each workflow job asks for exactly what it needs (`packages: write` for the image, `contents: write` for the release); nothing else needs write access.
- [ ] **Settings → Code security:** enable *Dependency graph*, *Dependabot alerts* and *Dependabot security updates*. Version updates are already configured in `.github/dependabot.yml`.
- [ ] Labels for grouped release notes (Dependabot uses `dependencies`):
  ```bash
  for l in dependencies feature bug breaking skip-changelog; do gh label create "$l" --force; done
  ```
- [ ] *(Recommended)* **Settings → Rules → Rulesets → New branch ruleset** for `main`: require a pull request and the status checks `Go tests + lint`, `UI tests + type check`, `Build, smoke + Playwright e2e`, `Docker build (amd64 + arm64, no push)`, `Lint workflows`.
- [ ] *(Recommended)* A **tag ruleset** for `v*` that restricts creation to you, so only you can trigger releases.

No secrets are needed: everything uses the built-in `GITHUB_TOKEN`.

## 3. First release

- [ ] Check that CI is green on `main` (Actions tab).
- [ ] Tag and push:
  ```bash
  git push origin v5.0.0          # already created by prepare-github.sh
  ```
  Try a pre-release first if you like (`v5.0.1-rc.1`): it publishes everything but doesn't move `:latest`.
- [ ] Watch **Actions → release**: `ci` → `image` + `binaries` → `release`.
- [ ] **Make the container package public** (new GHCR packages start private): your profile → **Packages** → `nulldrive` → **Package settings** → *Change visibility* → **Public**. Check that it shows as linked to the repo (the image's `org.opencontainers.image.source` label does this automatically).
- [ ] Verify on any machine with Docker:
  ```bash
  docker run --rm ghcr.io/you/nulldrive:5.0.0 version           # prints v5.0.0 and the commit
  curl -O https://raw.githubusercontent.com/you/nulldrive/main/deploy/docker-compose.yml
  docker compose up -d                                           # → http://localhost:8080 shows the setup wizard
  docker inspect --format '{{.State.Health.Status}}' nulldrive   # → healthy (after ~15 s)
  ```
- [ ] Check the Release page: 6 archives + `SHA256SUMS.txt`, notes with the install snippet.

## 4. Optional extras

- [ ] README badges: `![CI](https://github.com/you/nulldrive/actions/workflows/ci.yml/badge.svg)` and the latest release.
- [ ] Submit the NAS templates upstream: `deploy/SUBMITTING.md`.

## If something fails

| Failure | Likely cause |
|---|---|
| `denied: permission_denied` / `403` pushing to ghcr.io | Workflow permissions were tightened below the default, or the package already exists under another repo: Package settings → *Manage Actions access* → add this repo with **Write**. |
| `invalid reference format: repository name must be lowercase` | The repo or owner name has capitals: the workflow lowercases them, but check any hard-coded `OWNER` you replaced. |
| Docker build: `go.sum: no such file` | `go.sum` wasn't committed (step 1). |
| `gh release create`: `tag … not found` | The tag was pushed before its commit, or is lightweight on a different commit: re-push the tag. |
| `resource not accessible by integration` on release | Workflow permissions set to read-only *and* restricted at org level: allow `contents: write` for workflows in the org settings. |
