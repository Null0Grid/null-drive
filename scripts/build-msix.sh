#!/usr/bin/env bash
# Builds the Microsoft Store package (MSIX) of the desktop app:
#
#   scripts/build-msix.sh [version]     # after scripts/build-windows.sh
#
# Output: dist/NullDrive_<v>_x64.msix
#
# Package identity comes from Partner Center (Null Drive › Product
# management › Product identity; Store ID 9P3NQGJ8ZZMG). The defaults are
# those values, so every build is ready to upload. Override for test builds:
#   MSIX_IDENTITY_NAME            default NULLGRID.NullDrive
#   MSIX_PUBLISHER                default CN=8685350B-5C4C-4422-9CDF-8AEB87F50B2F
#                                 (a sideload test must be signed by a
#                                 certificate with this subject: override it
#                                 with your test certificate's, see STORE.md)
#   MSIX_PUBLISHER_DISPLAY_NAME   default NULLGRID
#
# For the Store, upload the unsigned .msix: the Store signs it. To install
# a test build on a PC (sideloading), sign it: set MSIX_CERT_PFX (+
# MSIX_CERT_PASSWORD) to a code-signing certificate whose subject equals
# MSIX_PUBLISHER, and trust that certificate on the PC (see INSTALL.md).
#
# Packing tool, whichever is found:
#   Windows: MakeAppx.exe + MakePri.exe from the Windows SDK (what CI and
#            the Store build use; MakePri indexes the scaled icons)
#   elsewhere: makemsix from github.com/microsoft/msix-packaging (built with
#            `./makelinux.sh --pack`); no resources.pri, so Windows uses the
#            100% icons. Fine for testing, use the Windows build for the Store.
# MAKEAPPX / MAKEPRI / MAKEMSIX override the paths.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD

VERSION="${1:-$(git describe --tags --match 'v*' 2>/dev/null || echo v0.0.0-dev)}"
VERSION="${VERSION#v}"
NUM="${VERSION%%-*}"
IFS=. read -r MA MI PA <<<"$NUM"
# The Store requires the fourth part to be 0.
VERSION4="${MA:-0}.${MI:-0}.${PA:-0}.0"

NAME="${MSIX_IDENTITY_NAME:-NULLGRID.NullDrive}"
PUBLISHER="${MSIX_PUBLISHER:-CN=8685350B-5C4C-4422-9CDF-8AEB87F50B2F}"
PUBLISHER_DISPLAY="${MSIX_PUBLISHER_DISPLAY_NAME:-NULLGRID}"

EXE="$ROOT/client/build/bin/NullDrive.exe"
SRC="$ROOT/client/build/windows/msix"
OUT="$ROOT/dist"
STAGE="$OUT/msix-stage"
MSIX="$OUT/NullDrive_${VERSION}_x64.msix"

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { echo "build-msix: $*" >&2; exit 1; }

[[ -f "$EXE" ]] || die "$EXE not found: run scripts/build-windows.sh first"
for v in "$NAME" "$PUBLISHER" "$PUBLISHER_DISPLAY"; do
  [[ "$v" != *[\<\>\&\"]* ]] || die "identity values can't contain < > & \""
done
[[ "$NAME" =~ ^[A-Za-z0-9.-]{3,50}$ ]] || die "MSIX_IDENTITY_NAME: 3-50 letters, digits, dots, hyphens"
[[ "$PUBLISHER" == CN=* ]] || die "MSIX_PUBLISHER must be a distinguished name (CN=...)"

step "Stage"
rm -rf "$STAGE" && mkdir -p "$STAGE/Assets"
cp "$EXE" "$STAGE/NullDrive.exe"
cp "$ROOT/LICENSE" "$STAGE/LICENSE.txt"
cp "$SRC"/Assets/*.png "$STAGE/Assets/"
# Render the manifest: fill in the identity, drop the explanatory comment.
PY=$(command -v python3 || command -v python) || die "Python 3 is needed"
"$PY" - "$SRC/AppxManifest.xml" "$STAGE/AppxManifest.xml" "$NAME" "$PUBLISHER" "$PUBLISHER_DISPLAY" "$VERSION4" <<'PY'
import re, sys
src, dst, name, pub, disp, ver = sys.argv[1:]
s = open(src, encoding="utf-8").read()
s = re.sub(r"<!--.*?-->\s*", "", s, count=1, flags=re.S)
for k, v in {"IDENTITY_NAME": name, "PUBLISHER": pub, "PUBLISHER_DISPLAY_NAME": disp, "VERSION": ver}.items():
    s = s.replace("{{" + k + "}}", v)
if "{{" in s:
    sys.exit("unfilled placeholder in AppxManifest.xml")
open(dst, "w", encoding="utf-8").write(s)
PY
echo "identity: $NAME, $PUBLISHER, $VERSION4"

find_sdk_tool() { # newest Windows SDK x64 copy of $1
  local hit
  # shellcheck disable=SC2012 # SDK version folders: plain names
  hit=$(ls -d "/c/Program Files (x86)/Windows Kits/10/bin/"10.*/x64/"$1" 2>/dev/null | sort -V | tail -1 || true)
  [[ -n "$hit" ]] && echo "$hit"
}
MAKEAPPX="${MAKEAPPX:-$(command -v makeappx.exe || find_sdk_tool makeappx.exe || true)}"
MAKEPRI="${MAKEPRI:-$(command -v makepri.exe || find_sdk_tool makepri.exe || true)}"
MAKEMSIX="${MAKEMSIX:-$(command -v makemsix || true)}"

step "Pack"
rm -f "$MSIX"
if [[ -n "$MAKEAPPX" ]]; then
  if [[ -n "$MAKEPRI" ]]; then
    # Index the scale-/targetsize- icon variants into resources.pri.
    (cd "$STAGE" && MSYS2_ARG_CONV_EXCL='*' "$MAKEPRI" createconfig /cf priconfig.xml /dq en-US /pv 10.0.0 /o >/dev/null &&
      MSYS2_ARG_CONV_EXCL='*' "$MAKEPRI" new /pr . /cf priconfig.xml /mn AppxManifest.xml /of resources.pri /o >/dev/null &&
      rm priconfig.xml)
  fi
  # Git Bash (MSYS) rewrites arguments that look like Unix paths: "/d" would
  # become "D:/" and MakeAppx would fail with exit code 87 (invalid
  # parameter). MSYS2_ARG_CONV_EXCL='*' passes the switches through as is.
  MSYS2_ARG_CONV_EXCL='*' "$MAKEAPPX" pack /d "$(cygpath -w "$STAGE" 2>/dev/null || echo "$STAGE")" /p "$(cygpath -w "$MSIX" 2>/dev/null || echo "$MSIX")" /o
elif [[ -n "$MAKEMSIX" ]]; then
  "$MAKEMSIX" pack -d "$STAGE" -p "$MSIX"
else
  die "no MakeAppx.exe (Windows SDK) or makemsix found; see the comment at the top of this script"
fi

if [[ -n "${MSIX_CERT_PFX:-}" ]]; then
  step "Sign (for sideloading; the Store signs uploads itself)"
  command -v osslsigncode >/dev/null || die "osslsigncode not found"
  osslsigncode sign -pkcs12 "$MSIX_CERT_PFX" ${MSIX_CERT_PASSWORD:+-pass "$MSIX_CERT_PASSWORD"} \
    -h sha256 -t http://timestamp.digicert.com -in "$MSIX" -out "$MSIX.signed" ||
    osslsigncode sign -pkcs12 "$MSIX_CERT_PFX" ${MSIX_CERT_PASSWORD:+-pass "$MSIX_CERT_PASSWORD"} \
      -h sha256 -in "$MSIX" -out "$MSIX.signed" # offline: no timestamp
  mv "$MSIX.signed" "$MSIX"
fi

rm -rf "$STAGE"
step "Done"
ls -l "$MSIX"
(cd "$OUT" && sha256sum "$(basename "$MSIX")")
