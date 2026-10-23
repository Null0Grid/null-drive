#!/usr/bin/env bash
# until_ takes a condition as a string and evaluates it later.
# shellcheck disable=SC2016
# Tests the Windows installer and nulldrive:// link handling under Wine,
# with the real NullDrive.exe and Setup.exe:
#
#   scripts/build-windows.sh && scripts/wine-test.sh [Setup.exe]
#
# Checks: the installer registers nulldrive:// per user (HKCU); the real exe
# hands a link to a running copy and exits, sending only `--url <link>` even
# when launched with --forget/--quit; a link fired through ShellExecute (as
# browsers do) launches `NullDrive.exe --url <link>`; a link crafted to break
# out of the quotes yields extra arguments (which the app ignores, see
# client/link_test.go); the uninstaller removes its own registration and
# leaves another copy's alone; a reinstall takes the scheme back.
#
# Needs wine (64- and 32-bit: the NSIS installer is 32-bit), xvfb-run, Go.
# Uses its own Wine prefix (WINEPREFIX to override). What Wine can't show:
# the app's window (no WebView2), MSIX packages.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2
# shellcheck disable=SC2012 # newest installer by time
SETUP="${1:-$(ls -t dist/NullDrive-*-Setup.exe 2>/dev/null | head -1)}"
[[ -f "$SETUP" ]] || { echo "no installer: run scripts/build-windows.sh first" >&2; exit 2; }
SETUP=$(realpath "$SETUP")

if [[ -z "${DISPLAY:-}" && -z "${WINE_TEST_UNDER_XVFB:-}" ]]; then
  exec env WINE_TEST_UNDER_XVFB=1 xvfb-run -a "$0" "$SETUP"
fi

TOOLS=$(mktemp -d)
trap 'rm -rf "$TOOLS"' EXIT
export WINEPREFIX="${WINEPREFIX:-$HOME/.cache/nulldrive-wine-test}" WINEDEBUG=-all
for t in runningcopy recorder shellexec; do
  (cd client && GOOS=windows GOARCH=amd64 go build -o "$TOOLS/$t.exe" "./internal/winetest/$t") || exit 2
done

ID=d28fc8c06bfa5b3229e9cada9b33e866
KEY='HKCU\Software\Classes\nulldrive'
pass=0
fail=0
ok() { if [ "$1" = 0 ]; then echo "ok   $2"; pass=$((pass + 1)); else echo "FAIL $2"; fail=$((fail + 1)); fi; }
# is <test...>: prints 0 if the test passes, 1 if not (for ok).
is() { if "$@"; then echo 0; else echo 1; fi; }
until_() { for _ in $(seq 80); do eval "$1" && return 0; sleep 0.5; done; return 1; }
# Wine's reg prints "Unable to find..." on stdout: drop it.
q() { wine reg query "$KEY" /s 2>/dev/null | tr -d '\r' | grep -v '^reg: Unable to find'; }

wine reg delete "$KEY" /f >/dev/null 2>&1
wine "$SETUP" /S
INST=$(find "$WINEPREFIX/drive_c/users" -path '*AppData/Local/Programs/Null Drive' -type d | head -1)
until_ '[ -e "$INST/NullDrive.exe" ]'
REG=$(q)
echo "$REG" | grep -q 'URL Protocol'
ok $? "installer registers nulldrive:// (URL Protocol)"
echo "$REG" | grep -qF 'NullDrive.exe" --url "%1"'
ok $? "  command: \"…\\NullDrive.exe\" --url \"%1\""
echo "$REG" | grep -q 'DefaultIcon'
ok $? "  icon"

# The real exe hands a link to a running copy and exits.
OUT=$TOOLS/runningcopy.txt
for form in "nulldrive://open?id=$ID" "--url nulldrive://open?id=$ID" "--forget --url nulldrive://open?id=$ID --quit"; do
  : >"$OUT"
  wine "$TOOLS/runningcopy.exe" >"$OUT" 2>&1 &
  RC=$!
  until_ "grep -q listening '$OUT'"
  # shellcheck disable=SC2086 # the form is several arguments on purpose
  timeout 30 wine "$INST/NullDrive.exe" $form
  rc=$?
  until_ "grep -q GOT '$OUT'"
  got=$(tr -d '\r' <"$OUT" | grep GOT)
  wait $RC 2>/dev/null
  echo "$got" | grep -qF "\"Args\":[\"--url\",\"nulldrive://open?id=$ID\"]"
  ok $? "running copy receives only the link: $form"
  ok "$(is [ "$rc" = 0 ])" "  and the new process exits at once"
done

# A click: Windows runs the registered command. (Windows resolves schemes
# through HKEY_CLASSES_ROOT, which merges HKLM and HKCU\Software\Classes;
# Wine 9's ShellExecute reads only the HKLM half, so mirror the installer's
# per-user command there for this step.)
CMD=$(wine reg query "$KEY\\shell\\open\\command" /ve 2>/dev/null | tr -d '\r' | sed -n 's/.*REG_SZ *//p')
wine reg add 'HKLM\Software\Classes\nulldrive' /v "URL Protocol" /d "" /f >/dev/null 2>&1
wine reg add 'HKLM\Software\Classes\nulldrive\shell\open\command' /ve /d "$CMD" /f >/dev/null 2>&1
cp "$INST/NullDrive.exe" "$TOOLS/NullDrive.real.exe"
cp "$TOOLS/recorder.exe" "$INST/NullDrive.exe"
L="$WINEPREFIX/drive_c/launched.json"
rm -f "$L"
wine "$TOOLS/shellexec.exe" "nulldrive://open?id=$ID"
until_ "[ -s '$L' ]"
ok "$(is [ "$(cat "$L" 2>/dev/null)" = "[\"--url\",\"nulldrive://open?id=$ID\"]" ])" "a clicked link launches NullDrive.exe --url <link>"
rm -f "$L"
wine "$TOOLS/shellexec.exe" 'nulldrive://open?id=x" --forget "y'
until_ "[ -s '$L' ]"
echo "     a crafted link launches: $(cat "$L" 2>/dev/null)  (the app ignores all but the link)"
cp "$TOOLS/NullDrive.real.exe" "$INST/NullDrive.exe"
wine reg delete 'HKLM\Software\Classes\nulldrive' /f >/dev/null 2>&1

UN="$INST/Uninstall Null Drive.exe"
wine "$UN" /S
until_ '[ ! -e "$INST/NullDrive.exe" ]'
until_ '[ -z "$(q)" ]'
ok $? "uninstall removes nulldrive://"

wine "$SETUP" /S
until_ '[ -e "$INST/NullDrive.exe" ]'
wine reg add "$KEY\\shell\\open\\command" /ve /d '"C:\Portable\NullDrive.exe" --url "%1"' /f >/dev/null 2>&1
wine "$UN" /S
until_ '[ ! -e "$INST/NullDrive.exe" ]'
sleep 2
q | grep -qF 'C:\Portable\NullDrive.exe'
ok $? "uninstall leaves another copy's registration alone"

wine "$SETUP" /S
until_ "q | grep -qF 'Programs\\Null Drive\\NullDrive.exe\" --url'"
ok $? "installing again takes the scheme back"
wine "$UN" /S
until_ '[ -z "$(q)" ]'

echo "passed $pass, failed $fail"
[ "$fail" = 0 ]
