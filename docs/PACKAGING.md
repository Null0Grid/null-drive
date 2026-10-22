# Publishing to Flathub, the Snap Store and the AUR

Every release already has the files these stores take, built and tested by the release workflow. Getting them into each store is a one-time sign-up and submission, done with your own account; after that, each release is one small update. GitHub Actions builds and attaches everything; nothing here runs automatically.

| Store | What | Release file(s) | First time | Each release |
|---|---|---|---|---|
| [Flathub](#flathub) | desktop app | `dev.nullgrid.NullDrive.yml` (manifest), `NullDrive-<v>-<arch>.flatpak` | pull request to flathub/flathub, review | replace the manifest in the app's Flathub repo |
| [Snap Store](#snap-store) | server | `nulldrive_<v>_<arch>.snap` | register the name `nulldrive` | `snapcraft upload` both snaps |
| [AUR](#aur) | server + desktop app | `nulldrive_<v>_aur.tar.gz` (`PKGBUILD` + `.SRCINFO`) | create both packages with a git push | copy the new files, push |

Install instructions for people using these are in [INSTALL.md](INSTALL.md). The design is in [DESIGN.md](DESIGN.md#more-linux-formats-flatpak-snap-aur).

## Flathub

App id: **`dev.nullgrid.NullDrive`**. Flathub checks that you control the domain in the id, `nullgrid.dev`, before it shows the app as verified.

**First submission** (once, about an hour plus the review):
1. Read [Flathub's submission requirements](https://docs.flathub.org/docs/for-app-authors/requirements). Points that apply here: the app is proprietary (`LicenseRef-proprietary`) but freely redistributable, which Flathub accepts; it repackages the release binary, which is allowed for an app whose source is public.
2. Fork [flathub/flathub](https://github.com/flathub/flathub), branch from `new-pr`, and add `dev.nullgrid.NullDrive.yml` from the latest release at the top level.
3. Open a pull request against `new-pr`, titled "Add dev.nullgrid.NullDrive". A bot builds it; comment `bot, build` to rebuild.
4. Reviewers often ask about permissions. The answers are the comments in the manifest. `--filesystem=home` is for the sync folder, which can be anywhere. `--own-name=org.kde.StatusNotifierItem-2-1` is the tray icon (the name has the process id in it, which is always 2 in the sandbox). The `SingleInstance` name shows the running window instead of starting a second copy.
5. Once merged, Flathub creates `github.com/flathub/dev.nullgrid.NullDrive` and invites you. Verify the domain there (Flathub's developer portal: a token at `https://nullgrid.dev/.well-known/org.flathub.VerifiedApps.txt`).

**Each release:** in `flathub/dev.nullgrid.NullDrive`, replace `dev.nullgrid.NullDrive.yml` with the one attached to the new release. Its URLs and sha256 already point at that release. Commit to `master` (or open a PR to see the test build first). Flathub publishes it in a few hours.

The manifest uses the GNOME runtime, which provides WebKitGTK 4.1. CI checks the library is there; when Flathub retires that runtime version, raise `RUNTIME_VERSION` in `scripts/flatpak-manifest.py`.

## Snap Store

Snap name: **`nulldrive`** (the server).

**First time:**
1. Create an account at [snapcraft.io](https://snapcraft.io) and run `snapcraft login`.
2. `snapcraft register nulldrive`. If the name is taken, pick another, then change `name:` in `scripts/build-snap.sh` and the docs.
3. A strict snap with a service doesn't need a store review. Snaps with names of known brands, or needing special interfaces, do; this one uses only `network` and `network-bind`.

**Each release:**
```bash
snapcraft upload --release=stable nulldrive_<v>_amd64.snap
snapcraft upload --release=stable nulldrive_<v>_arm64.snap
```
Then `sudo snap install nulldrive` works without `--dangerous`, and installs update themselves. To test first: `--release=candidate`, then `snapcraft release nulldrive <revision> stable`.

## AUR

Packages: **`nulldrive-bin`** (server) and **`nulldrive-desktop-bin`** (desktop app). `-bin` is the AUR's suffix for packages of prebuilt binaries.

**First time:**
1. Create an account at [aur.archlinux.org](https://aur.archlinux.org) and add your SSH public key in *My Account*.
2. For each package (an AUR package is a git repository that you create by pushing to it):
   ```bash
   tar -xzf nulldrive_<v>_aur.tar.gz
   git clone ssh://aur@aur.archlinux.org/nulldrive-bin.git aur-nulldrive-bin
   cp nulldrive-bin/* nulldrive-bin/.SRCINFO aur-nulldrive-bin/
   cd aur-nulldrive-bin && git add -A && git commit -m "nulldrive-bin <v>" && git push
   ```
   Repeat with `nulldrive-desktop-bin`.

**Each release:** the same copy, commit and push into the existing clones. The `.SRCINFO` was written by `makepkg --printsrcinfo` and must be committed with the `PKGBUILD`; the AUR rejects a push where they disagree.

CI builds both packages with `makepkg`, installs them with `pacman` in an Arch container, and runs the server, so a PKGBUILD that doesn't build never reaches a release.
