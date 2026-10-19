#!/usr/bin/env python3
"""Write winget manifests (schema 1.6, multi-file) for one release.

    python3 scripts/winget-manifest.py --version 0.7.0 --sha256 <HEX> \\
        --url https://github.com/OWNER/nulldrive/releases/download/v0.7.0/NullDrive-0.7.0-Setup.exe \\
        --repo OWNER/nulldrive --out dist/winget

Submitting: fork microsoft/winget-pkgs, copy the three files to
manifests/<first letter>/<Publisher>/NullDrive/<version>/ and open a PR
(`wingetcreate submit dist/winget` does the same). docs/INSTALL.md has the
checklist. The package identifier is "<GitHub owner>.NullDrive".
"""
from __future__ import annotations

import argparse
import os

SCHEMA = "1.6.0"


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--version", required=True)
    ap.add_argument("--sha256", required=True)
    ap.add_argument("--url", required=True)
    ap.add_argument("--repo", required=True, help="owner/name on GitHub")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()

    owner = a.repo.split("/")[0]
    publisher = owner if owner != "OW" + "NER" else "NullDrive"  # placeholder until prepare-github.sh
    pid = f"{publisher}.NullDrive"
    home = f"https://github.com/{a.repo}"
    os.makedirs(a.out, exist_ok=True)
    header = "# yaml-language-server: $schema=https://aka.ms/winget-manifest.{kind}.{SCHEMA}.schema.json\n"

    files = {
        f"{pid}.yaml": header.format(kind="version", SCHEMA=SCHEMA) + f"""
PackageIdentifier: {pid}
PackageVersion: {a.version}
DefaultLocale: en-US
ManifestType: version
ManifestVersion: {SCHEMA}
""",
        f"{pid}.installer.yaml": header.format(kind="installer", SCHEMA=SCHEMA) + f"""
PackageIdentifier: {pid}
PackageVersion: {a.version}
MinimumOSVersion: 10.0.17763.0
InstallerType: nullsoft
Scope: user
InstallModes:
  - interactive
  - silent
  - silentWithProgress
InstallerSwitches:
  Silent: /S
  SilentWithProgress: /S
UpgradeBehavior: install
Dependencies:
  PackageDependencies:
    - PackageIdentifier: Microsoft.EdgeWebView2Runtime
Installers:
  - Architecture: x64
    InstallerUrl: {a.url}
    InstallerSha256: {a.sha256.upper()}
ManifestType: installer
ManifestVersion: {SCHEMA}
""",
        f"{pid}.locale.en-US.yaml": header.format(kind="defaultLocale", SCHEMA=SCHEMA) + f"""
PackageIdentifier: {pid}
PackageVersion: {a.version}
PackageLocale: en-US
Publisher: {publisher}
PublisherUrl: https://github.com/{owner}
PackageName: Null Drive
PackageUrl: {home}
License: MIT
LicenseUrl: {home}/blob/main/LICENSE
ShortDescription: Self-hosted file sync client. Like Dropbox, but you own the server.
Description: |-
  Null Drive keeps a folder on your computer in sync with your own Null Drive
  server (Docker on a NAS, mini PC, Raspberry Pi or VPS). Delta sync, version
  history, conflict copies instead of silent overwrites, LAN discovery,
  pairing codes and optional end-to-end encryption.
Moniker: nulldrive
Tags:
  - sync
  - self-hosted
  - file-sync
  - backup
  - homelab
ReleaseNotesUrl: {home}/releases/tag/v{a.version}
ManifestType: defaultLocale
ManifestVersion: {SCHEMA}
""",
    }
    for name, body in files.items():
        with open(os.path.join(a.out, name), "w") as f:
            f.write(body.lstrip("\n") if not body.startswith("#") else body)
        print("wrote", os.path.join(a.out, name))


if __name__ == "__main__":
    main()
