#!/usr/bin/env bash
# One-time: make this repository yours before the first push to GitHub.
#
#   scripts/prepare-github.sh <github-username> "<Your Name>"
#
#   1. replaces the placeholders: OWNER (image names, links, NAS templates),
#      the Go module path github.com/nulldrive/nulldrive, and the LICENSE name
#   2. generates go.sum for both Go modules (go mod tidy). It isn't committed
#      yet because it must come from the real module proxy, and CI and the
#      Docker build need it
#   3. runs the Go tests
#   4. commits all of that and tags v5.0.0
#
# Then: create an empty repo named "nulldrive" on GitHub (no README, no
# license) and push, as the script prints at the end. Needs git, Go 1.24+,
# perl. Works on Linux, macOS and Git Bash on Windows.
set -euo pipefail
cd "$(dirname "$0")/.."

USER_RAW="${1:?usage: scripts/prepare-github.sh <github-username> \"<Your Name>\"}"
NAME="${2:?usage: scripts/prepare-github.sh <github-username> \"<Your Name>\"}"
GH_USER="$(printf '%s' "$USER_RAW" | tr '[:upper:]' '[:lower:]')" # GHCR image names must be lowercase
VERSION="${VERSION:-v5.0.0}"

command -v git >/dev/null || { echo "git is required" >&2; exit 1; }
command -v perl >/dev/null || { echo "perl is required" >&2; exit 1; }
[[ "${SKIP_GO:-}" == 1 ]] || command -v go >/dev/null || { echo "Go 1.24+ is required (https://go.dev/dl/)" >&2; exit 1; }
[[ -d .git ]] || { echo "run this from the repository (no .git found)" >&2; exit 1; }
if ! grep -rqI --exclude-dir=.git --exclude-dir=node_modules --exclude=prepare-github.sh 'OWNER' . ; then
  echo "No OWNER placeholders left: this repository was already prepared." >&2
  exit 1
fi

echo "==> Placeholders → $GH_USER / $NAME"
files() { grep -rlI --exclude-dir=.git --exclude-dir=node_modules --exclude="prepare-github.sh" -e "$1" . || true; }
export GH_USER NAME
# Go module path first (it contains no OWNER), then the rest.
files 'github.com/nulldrive/nulldrive' | while read -r f; do
  perl -pi -e 's#github\.com/nulldrive/nulldrive#github.com/$ENV{GH_USER}/nulldrive#g' "$f"
done
files 'OWNER' | while read -r f; do
  perl -pi -e 's/<OWNER NAME>/$ENV{NAME}/g; s/OWNER/$ENV{GH_USER}/g' "$f"
done
files '<owner>' | while read -r f; do perl -pi -e 's/<owner>/$ENV{GH_USER}/g' "$f"; done

if [[ "${SKIP_GO:-}" != 1 ]]; then
  echo "==> go.sum (go mod tidy)"
  GOWORK=off go mod tidy
  (cd client && GOWORK=off go mod tidy)
  echo "==> Go tests"
  GOWORK=off go test ./... >/dev/null && echo "    ok"
fi

echo "==> Commit and tag $VERSION"
git add -A
git -c user.name="$NAME" commit -q -m "Prepare for github.com/$GH_USER/nulldrive

Owner placeholders, module path and LICENSE set; go.sum generated."
git tag -a "$VERSION" -m "Null Drive ${VERSION#v}"
git log --oneline | head -3

cat <<EOF

Done. Now:
  1. On GitHub: New repository → name "nulldrive" → Public → no README/license → Create.
  2. Push:
       git remote add origin https://github.com/$GH_USER/nulldrive.git
       git push -u origin main
       git push origin $VERSION        # starts the release workflow (image, binaries, Windows app)
  3. Finish the settings in docs/GITHUB-SETUP.md (Actions permissions, make the image public).
EOF
