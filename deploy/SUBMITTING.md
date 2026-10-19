# Publishing Null Drive to NAS app stores

All templates use the placeholder `OWNER`. Replace it with your **lowercase** GitHub username (GHCR image names must be lowercase), commit, and make sure a release image exists on GHCR and is **public** before submitting anywhere:

```bash
grep -rl OWNER deploy | xargs sed -i 's/OWNER/yourname/g'
```

Each store wants a stable support URL and screenshots; use the GitHub repo's Issues page and the images in `docs/screenshots/`.

---

## CasaOS / ZimaOS

**Template:** `casaos/Apps/NullDrive/docker-compose.yml` (AppStore **v2** format: a normal compose file plus a top-level `x-casaos` block).

1. Fork <https://github.com/IceWhaleTech/CasaOS-AppStore>.
2. Copy `casaos/Apps/NullDrive/` (compose + `icon.png`) to `Apps/NullDrive/` in the fork.
3. Validate locally:
   ```bash
   docker compose -f Apps/NullDrive/docker-compose.yml config -q
   ./scripts/build_dist.sh          # must finish without errors and produce dist/apps/io.github.<you>.nulldrive/
   ```
4. Open a PR saying it's a **new app**, what it does, and how you validated it. CI checks the compose file and build.
5. On every release: bump `image:` tag, `x-casaos.version`, `update_at` and `release_notes` in the same PR.

Rules that trip people up (from the store's protocol reference): `port_map` must be a quoted string, `title`/`tagline`/`description` are locale objects (`en_US:`), `category` must be one of the fixed list (we use `Productivity`), and `id` must be reverse-domain style.

**Before your own store listing lands**, CasaOS users can install it directly: App Store → Custom Install → Import → paste the compose file.

**Why the CasaOS template runs as `user: "0:0"`:** CasaOS creates `/DATA/AppData/...` owned by root, so the image's default user (65532) couldn't write there. It runs as root but with **all capabilities dropped**, `no-new-privileges` and a read-only root filesystem. Without `CAP_DAC_OVERRIDE`, root inside the container can only write to files it owns.

## Unraid (Community Applications)

**Template:** `unraid/nulldrive.xml`, which runs as `99:100` (nobody:users, Unraid's appdata owner), read-only root, all capabilities dropped, 30 s stop timeout.

1. Create a **public GitHub repo for templates** (e.g. `yourname/unraid-templates`), or keep the XML in this repo.
2. Start from the CA starter repository linked on <https://ca.unraid.net/submit> and add `nulldrive.xml`. One XML file per app.
3. Make sure `<TemplateURL>` and `<Icon>` point at the raw GitHub URLs of those files, and `<Support>` at a page where users can get help (GitHub Issues works; many authors also open a thread in the Unraid forum's *Docker Containers* section).
4. On <https://ca.unraid.net/submit/new>: **Validate**, fix anything reported, then **Scan**, and submit the repository for moderator review.
5. Test before submitting: on an Unraid box, Docker → Add Container → Template repositories → add your repo URL, then install from the template.

Field reference: <https://ca.unraid.net/submit/help/xml-field-reference>.

## TrueNAS SCALE

There are two routes:

### a) Install via YAML (works today, no review)
`truenas/docker-compose.yaml`, for TrueNAS SCALE 24.10+: Apps → Discover Apps → ⋮ → **Install via YAML**. It runs as the built-in `apps` user (568); give that user ownership of the dataset first. Instructions are in the file header and in `docs/INSTALL.md`.

### b) Official catalog (community train)
The catalog is <https://github.com/truenas/apps>; apps are Jinja-templated compose files built on TrueNAS's rendering library, so the full app has to be assembled inside their repo with their tooling. `truenas/catalog/nulldrive/` holds the parts that are specific to Null Drive (`app.yaml`, `ix_values.yaml`).

1. Read their [CONTRIBUTIONS.md](https://github.com/truenas/apps/blob/master/CONTRIBUTIONS.md). Open an issue (or draft PR) first to say you're adding Null Drive.
2. In a fork: copy a similar **single-container** community app under `ix-dev/community/` to `ix-dev/community/nulldrive/`, delete any `app_migrations.yaml`, `migrations/` and `deprecations.yaml`.
3. Replace its `app.yaml` and `ix_values.yaml` with ours; set `lib_version` to the latest library and run `devbox run copy-lib` (fills `lib_version_hash` and vendors the library; commit it).
4. Adapt `questions.yaml` (web port, one storage entry for `/data`, run-as user 568) and the compose template (single container, `NULLDRIVE_DATA_DIR=/data`, healthcheck `["/nulldrive","healthcheck"]`).
5. Test with their CI script locally (see "Local Testing" in their guide), attach the icon and screenshots to the PR (they host them on their CDN), and submit against the **community** train.

---

## Release checklist for store listings

- [ ] GitHub release `vX.Y.Z` published, images `ghcr.io/<you>/nulldrive:X.Y.Z` and `:latest` exist and are public
- [ ] CasaOS: `image`, `version`, `update_at`, `release_notes` bumped
- [ ] Unraid: nothing to do for `:latest` (users update in the Docker tab); bump if pinned
- [ ] TrueNAS catalog: their Renovate bot bumps `ix_values.yaml` image tags automatically once the app is merged
