; Null Drive installer (NSIS 3). Built by scripts/build-windows.sh:
;
;   makensis -DVERSION=0.7.0 -DVERSION4=0.7.0.0 -DEXE=path\NullDrive.exe \
;            -DLICENSE=path\LICENSE -DOUTFILE=path\NullDrive-0.7.0-Setup.exe nulldrive.nsi
;
; Design choices (see docs/DESIGN.md › Windows packaging):
;  - Per-user install into %LOCALAPPDATA%\Programs\Null Drive, no admin
;    prompt. A sync client belongs to one user (like Dropbox or VS Code's
;    user installer), and updates never need elevation.
;  - Before replacing or removing files it asks a running copy to quit
;    ("NullDrive --quit"), so sync stops cleanly; it only force-closes the
;    app if that doesn't work within ~10 s.
;  - Uninstalling never touches the synced folder. Settings and saved
;    credentials are removed only if the user says so.
;  - nulldrive:// links (Null Search results: nulldrive://open?id=<id>) are
;    registered per user under HKCU\Software\Classes, no admin needed, with
;    the same command the app and the Store manifest use: the link always
;    arrives after "--url", so the app treats it as untrusted input. The
;    uninstaller removes the registration only if it still points here.
;  - The app needs the Microsoft Edge WebView2 runtime (part of Windows 11
;    and current Windows 10). If it's missing, the installer offers the
;    official download instead of bundling it.

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma
RequestExecutionLevel user

!ifndef VERSION
  !error "pass -DVERSION=x.y.z"
!endif

!define APP "Null Drive"
!define EXE_NAME "NullDrive.exe"
!define UNINST_NAME "Uninstall Null Drive.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\NullDrive"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define URL_KEY "Software\Classes\nulldrive"
!define WEBVIEW2_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!define WEBVIEW2_URL "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

Name "${APP}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\${APP}"
InstallDirRegKey HKCU "Software\${APP}" "InstallDir"
BrandingText "${APP} ${VERSION}"

VIProductVersion "${VERSION4}"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileDescription" "${APP} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "CompanyName" "${APP}"
VIAddVersionKey "LegalCopyright" "MIT License"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"

!define MUI_ICON "../icon.ico"
!define MUI_UNICON "../icon.ico"
!define MUI_WELCOMEFINISHPAGE_BITMAP "welcome.bmp"
!define MUI_UNWELCOMEFINISHPAGE_BITMAP "welcome.bmp"
!define MUI_HEADERIMAGE
!define MUI_HEADERIMAGE_RIGHT
!define MUI_HEADERIMAGE_BITMAP "header.bmp"
!define MUI_HEADERIMAGE_UNBITMAP "header.bmp"
!define MUI_ABORTWARNING

!define MUI_WELCOMEPAGE_TITLE "Install ${APP}"
!define MUI_WELCOMEPAGE_TEXT "Null Drive keeps a folder on this computer in sync with your own Null Drive server.$\r$\n$\r$\nIt installs for your Windows account only and doesn't need administrator rights.$\r$\n$\r$\nIf Null Drive is already running, it is closed for the update and its sync picks up where it left off."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE_NAME}"
!define MUI_FINISHPAGE_RUN_TEXT "Open Null Drive"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; Ask a running copy to quit, wait for the exe to be unlocked, force-close
; as a last resort. Works for both the installer and the uninstaller.
!macro StopRunningApp UN
Function ${UN}StopRunningApp
  IfFileExists "$INSTDIR\${EXE_NAME}" 0 done
  DetailPrint "Closing Null Drive if it's running..."
  ExecWait '"$INSTDIR\${EXE_NAME}" --quit'
  StrCpy $0 0
  loop:
    ClearErrors
    Rename "$INSTDIR\${EXE_NAME}" "$INSTDIR\${EXE_NAME}.check"
    ${IfNot} ${Errors}
      Rename "$INSTDIR\${EXE_NAME}.check" "$INSTDIR\${EXE_NAME}"
      Goto done
    ${EndIf}
    IntOp $0 $0 + 1
    ${If} $0 = 20
      DetailPrint "Null Drive didn't close; ending it."
      nsExec::Exec 'taskkill /F /IM ${EXE_NAME}'
      Pop $1
      Sleep 1000
      Goto done
    ${EndIf}
    Sleep 500
    Goto loop
  done:
FunctionEnd
!macroend
!insertmacro StopRunningApp ""
!insertmacro StopRunningApp "un."

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "Null Drive needs 64-bit Windows 10 or later." /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

Function CheckWebView2
  ; Evergreen runtime: per-machine (64- or 32-bit view) or per-user install.
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    DetailPrint "Microsoft Edge WebView2 runtime not found."
    MessageBox MB_YESNO|MB_ICONINFORMATION "Null Drive uses the Microsoft Edge WebView2 runtime, which isn't installed on this PC.$\r$\n$\r$\nOpen Microsoft's download page now? (Null Drive will ask again when it starts.)" /SD IDNO IDNO skip
      ExecShell "open" "${WEBVIEW2_URL}"
    skip:
  ${Else}
    DetailPrint "WebView2 runtime $0 found."
  ${EndIf}
FunctionEnd

Section "Null Drive" SecMain
  SectionIn RO
  Call StopRunningApp
  SetOutPath "$INSTDIR"
  File "/oname=${EXE_NAME}" "${EXE}"
  File "/oname=LICENSE.txt" "${LICENSE}"
  WriteUninstaller "$INSTDIR\${UNINST_NAME}"

  CreateShortcut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${EXE_NAME}" "" "$INSTDIR\${EXE_NAME}" 0

  ; nulldrive:// links. Replaces any older registration (e.g. a portable copy's).
  DeleteRegKey HKCU "${URL_KEY}"
  WriteRegStr HKCU "${URL_KEY}" "" "URL:${APP}"
  WriteRegStr HKCU "${URL_KEY}" "URL Protocol" ""
  WriteRegStr HKCU "${URL_KEY}\DefaultIcon" "" '"$INSTDIR\${EXE_NAME}",0'
  WriteRegStr HKCU "${URL_KEY}\shell\open\command" "" '"$INSTDIR\${EXE_NAME}" --url "%1"'
  ; Tell Explorer and browsers the association changed (SHCNE_ASSOCCHANGED).
  System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0, p 0, p 0)'

  WriteRegStr HKCU "Software\${APP}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${APP}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "${APP}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${EXE_NAME},0"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\${UNINST_NAME}"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\${UNINST_NAME}" /S'
  WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "https://github.com/OWNER/nulldrive"
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  SectionGetSize ${SecMain} $0
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" $0

  Call CheckWebView2
SectionEnd

Section "Uninstall"
  Call un.StopRunningApp

  ; Settings are only removed on request; the synced folder is never touched.
  MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 "Also remove Null Drive's settings and the sign-in saved on this PC?$\r$\n$\r$\nYour synced files stay where they are either way." /SD IDNO IDNO keep
    ExecWait '"$INSTDIR\${EXE_NAME}" --forget'
    RMDir /r "$APPDATA\${APP}"
  keep:

  DeleteRegValue HKCU "${RUN_KEY}" "NullDrive"
  ReadRegStr $0 HKCU "${URL_KEY}\shell\open\command" ""
  ${If} $0 == '"$INSTDIR\${EXE_NAME}" --url "%1"'
    DeleteRegKey HKCU "${URL_KEY}"
    System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0, p 0, p 0)'
  ${EndIf}
  Delete "$SMPROGRAMS\${APP}.lnk"
  Delete "$INSTDIR\${EXE_NAME}"
  Delete "$INSTDIR\LICENSE.txt"
  Delete "$INSTDIR\${UNINST_NAME}"
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\${APP}"
SectionEnd
