; CPA Cloud per-user installer. Built only by scripts/release-windows-installer.ps1.
; NSIS 3.12 source: https://sourceforge.net/projects/nsis/files/NSIS%203/3.12/

Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "x64.nsh"

!ifndef APP_VERSION
  !error "APP_VERSION is required"
!endif
!ifndef TARGET_ARCH
  !error "TARGET_ARCH is required"
!endif
!ifndef PAYLOAD_DIR
  !error "PAYLOAD_DIR is required"
!endif
!ifndef OUTPUT_DIR
  !error "OUTPUT_DIR is required"
!endif
!ifndef UNINSTALL_MANIFEST
  !error "UNINSTALL_MANIFEST is required"
!endif

!include "${UNINSTALL_MANIFEST}"

!define APP_NAME "CPA Cloud"
!define APP_ID "CPACloud.Launcher"
!define APP_MUTEX "Local\CPACloud.Launcher"
!define SETUP_MUTEX "Local\CPACloud.Setup.2D7C1C6E-20A9-46F4-9955-24465BD75DCB"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_ID}"

Name "${APP_NAME}"
Caption "${APP_NAME} ${APP_VERSION} Setup"
OutFile "${OUTPUT_DIR}\cpa-cloud_${APP_VERSION}_windows_${TARGET_ARCH}_Setup.exe"
InstallDir "$LOCALAPPDATA\Programs\CPA Cloud"
RequestExecutionLevel user
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show

!define MUI_ABORTWARNING
!define MUI_COMPONENTSPAGE_SMALLDESC
!define MUI_FINISHPAGE_RUN "$INSTDIR\CPACloud.Launcher.exe"
!define MUI_FINISHPAGE_RUN_NOTCHECKED
!define MUI_LANGDLL_REGISTRY_ROOT HKCU
!define MUI_LANGDLL_REGISTRY_KEY "Software\CPACloud"
!define MUI_LANGDLL_REGISTRY_VALUENAME "InstallerLanguage"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"

LangString LauncherRunning ${LANG_ENGLISH} "CPA Cloud is running. Exit the launcher before installing, upgrading, or uninstalling. The installer will not stop another process or overwrite files in use."
LangString LauncherRunning ${LANG_SIMPCHINESE} "CPA Cloud 正在运行。安装、升级或卸载前请先退出启动器。安装器不会结束其他进程，也不会覆盖正在使用的文件。"
LangString SetupRunning ${LANG_ENGLISH} "Another CPA Cloud installer is running for this user. Close it before continuing."
LangString SetupRunning ${LANG_SIMPCHINESE} "当前用户已有另一个 CPA Cloud 安装程序正在运行，请先关闭它。"
LangString WrongAMD64 ${LANG_ENGLISH} "This package contains the Windows x64 build. Download the Windows ARM64 setup for an ARM64 computer."
LangString WrongAMD64 ${LANG_SIMPCHINESE} "此安装包包含 Windows x64 版本。ARM64 电脑请下载 Windows ARM64 安装包。"
LangString WrongARM64 ${LANG_ENGLISH} "This package contains the Windows ARM64 build and can only be installed on native ARM64 Windows."
LangString WrongARM64 ${LANG_SIMPCHINESE} "此安装包包含 Windows ARM64 版本，只能安装到原生 ARM64 Windows。"
LangString CoreDescription ${LANG_ENGLISH} "CPA Cloud launcher, local server, web console, and required notices."
LangString CoreDescription ${LANG_SIMPCHINESE} "CPA Cloud 启动器、本机服务、网页后台及必要声明。"
LangString DesktopDescription ${LANG_ENGLISH} "Create an optional shortcut on the current user's desktop."
LangString DesktopDescription ${LANG_SIMPCHINESE} "在当前用户桌面创建可选快捷方式。"
LangString UnownedDirectory ${LANG_ENGLISH} "The fixed CPA Cloud installation folder exists without its ownership marker. Setup will not overwrite or remove files from that folder. Move it aside and try again."
LangString UnownedDirectory ${LANG_SIMPCHINESE} "固定的 CPA Cloud 安装目录已存在，但没有本程序的所有权标记。安装程序不会覆盖或删除其中的文件。请将该目录移走后重试。"
LangString UnsafeReparse ${LANG_ENGLISH} "Setup found a filesystem reparse point in an installation target or its ancestor and will not write or remove files through it:"
LangString UnsafeReparse ${LANG_SIMPCHINESE} "安装目标或其上级目录中存在文件系统重解析点。安装程序不会通过该路径写入或删除文件："
LangString SetupMutexFailed ${LANG_ENGLISH} "CPA Cloud Setup could not create its per-user coordination lock. Close other setup processes and try again."
LangString SetupMutexFailed ${LANG_SIMPCHINESE} "CPA Cloud 安装程序无法创建当前用户的协调锁。请关闭其他安装进程后重试。"

Var SetupMutexHandle

Function CheckLauncherNotRunning
  System::Call 'kernel32::OpenMutexW(i 0x00100000, i 0, w "${APP_MUTEX}") p.r0'
  ${If} $0 != 0
    System::Call 'kernel32::CloseHandle(p r0)'
    SetErrorLevel 10
    MessageBox MB_OK|MB_ICONEXCLAMATION "$(LauncherRunning)" /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

Function CheckArchitecture
!if "${TARGET_ARCH}" == "amd64"
  ${IfNot} ${IsNativeAMD64}
    SetErrorLevel 12
    MessageBox MB_OK|MB_ICONSTOP "$(WrongAMD64)" /SD IDOK
    Abort
  ${EndIf}
!else if "${TARGET_ARCH}" == "arm64"
  ${IfNot} ${IsNativeARM64}
    SetErrorLevel 12
    MessageBox MB_OK|MB_ICONSTOP "$(WrongARM64)" /SD IDOK
    Abort
  ${EndIf}
!else
  !error "TARGET_ARCH must be amd64 or arm64"
!endif
FunctionEnd

Function CheckInstallOwnership
  IfFileExists "$INSTDIR\*" 0 ownership_ok
  ClearErrors
  FileOpen $0 "$INSTDIR\.cpa-cloud-install" r
  IfErrors ownership_bad
  FileRead $0 $1
  FileClose $0
  StrCmp $1 "${APP_ID}$\r$\n" ownership_ok
ownership_bad:
  SetErrorLevel 13
  MessageBox MB_OK|MB_ICONSTOP "$(UnownedDirectory)" /SD IDOK
  Abort
ownership_ok:
FunctionEnd

Function CheckPathAndAncestorsNotReparse
  Exch $0
  Push $1
  Push $2
reparse_loop:
  System::Call 'kernel32::GetFileAttributesW(w r0) i.r1'
  ${If} $1 != -1
    IntOp $2 $1 & 0x400
    ${If} $2 != 0
      SetErrorLevel 14
      MessageBox MB_OK|MB_ICONSTOP "$(UnsafeReparse)$\r$\n$0" /SD IDOK
      Abort
    ${EndIf}
  ${EndIf}
  ${GetParent} "$0" $1
  StrCmp $1 "" reparse_done
  StrCmp $1 $0 reparse_done
  StrCpy $0 $1
  Goto reparse_loop
reparse_done:
  Pop $2
  Pop $1
  Pop $0
FunctionEnd

Function .onInit
  !insertmacro MUI_LANGDLL_DISPLAY
  SetShellVarContext current
  Call CheckArchitecture
  ; Capture GetLastError before any subsequent API or plug-in call can replace it.
  System::Call 'kernel32::CreateMutexW(p 0, i 0, w "${SETUP_MUTEX}") p.r0?e'
  Pop $1
  ${If} $0 = 0
    SetErrorLevel 15
    MessageBox MB_OK|MB_ICONSTOP "$(SetupMutexFailed)" /SD IDOK
    Abort
  ${EndIf}
  ${If} $1 = 183
    System::Call 'kernel32::CloseHandle(p r0)'
    SetErrorLevel 11
    MessageBox MB_OK|MB_ICONEXCLAMATION "$(SetupRunning)" /SD IDOK
    Abort
  ${EndIf}
  StrCpy $SetupMutexHandle $0
  Call CheckLauncherNotRunning
  Call CheckInstallOwnership
FunctionEnd

Function .onInstSuccess
  ; All payload and shortcut writes have completed before the finish page can
  ; optionally launch CPA Cloud.
  ${If} $SetupMutexHandle != 0
    System::Call 'kernel32::CloseHandle(p $SetupMutexHandle)'
    StrCpy $SetupMutexHandle 0
  ${EndIf}
FunctionEnd

Function un.onInit
  !insertmacro MUI_UNGETLANGUAGE
  SetShellVarContext current
  ; Capture GetLastError before any subsequent API or plug-in call can replace it.
  System::Call 'kernel32::CreateMutexW(p 0, i 0, w "${SETUP_MUTEX}") p.r0?e'
  Pop $1
  ${If} $0 = 0
    SetErrorLevel 15
    MessageBox MB_OK|MB_ICONSTOP "$(SetupMutexFailed)" /SD IDOK
    Abort
  ${EndIf}
  ${If} $1 = 183
    System::Call 'kernel32::CloseHandle(p r0)'
    SetErrorLevel 11
    MessageBox MB_OK|MB_ICONEXCLAMATION "$(SetupRunning)" /SD IDOK
    Abort
  ${EndIf}
  StrCpy $SetupMutexHandle $0
  Call un.CheckLauncherNotRunning
  Call un.CheckInstallOwnership
FunctionEnd

Function un.CheckInstallOwnership
  ClearErrors
  FileOpen $0 "$INSTDIR\.cpa-cloud-install" r
  IfErrors ownership_bad
  FileRead $0 $1
  FileClose $0
  StrCmp $1 "${APP_ID}$\r$\n" ownership_ok
ownership_bad:
  SetErrorLevel 13
  MessageBox MB_OK|MB_ICONSTOP "$(UnownedDirectory)" /SD IDOK
  Abort
ownership_ok:
FunctionEnd

Function un.CheckPathAndAncestorsNotReparse
  Exch $0
  Push $1
  Push $2
reparse_loop:
  System::Call 'kernel32::GetFileAttributesW(w r0) i.r1'
  ${If} $1 != -1
    IntOp $2 $1 & 0x400
    ${If} $2 != 0
      SetErrorLevel 14
      MessageBox MB_OK|MB_ICONSTOP "$(UnsafeReparse)$\r$\n$0" /SD IDOK
      Abort
    ${EndIf}
  ${EndIf}
  ${GetParent} "$0" $1
  StrCmp $1 "" reparse_done
  StrCmp $1 $0 reparse_done
  StrCpy $0 $1
  Goto reparse_loop
reparse_done:
  Pop $2
  Pop $1
  Pop $0
FunctionEnd

Function un.CheckLauncherNotRunning
  System::Call 'kernel32::OpenMutexW(i 0x00100000, i 0, w "${APP_MUTEX}") p.r0'
  ${If} $0 != 0
    System::Call 'kernel32::CloseHandle(p r0)'
    SetErrorLevel 10
    MessageBox MB_OK|MB_ICONEXCLAMATION "$(LauncherRunning)" /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

Section "CPA Cloud (required)" CoreSection
  SectionIn RO
  SetShellVarContext current
  ; Re-check immediately before writing: the welcome/components pages can stay
  ; open after .onInit. The launcher also refuses to start while SETUP_MUTEX is
  ; held, closing the other side of this race.
  Call CheckLauncherNotRunning
  Call CheckInstallOwnership
  Push "$INSTDIR\.cpa-cloud-install"
  Call CheckPathAndAncestorsNotReparse
  Push "$INSTDIR\Uninstall.exe"
  Call CheckPathAndAncestorsNotReparse
  !insertmacro CheckPayloadPathsNotReparse CheckPathAndAncestorsNotReparse
  SetOutPath "$INSTDIR"
  FileOpen $0 "$INSTDIR\.cpa-cloud-install" w
  FileWrite $0 "${APP_ID}$\r$\n"
  FileClose $0
  File /r "${PAYLOAD_DIR}\*"

  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "Software\CPACloud" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\CPACloud.Launcher.exe"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1

  CreateDirectory "$SMPROGRAMS\CPA Cloud"
  CreateShortcut "$SMPROGRAMS\CPA Cloud\CPA Cloud.lnk" "$INSTDIR\CPACloud.Launcher.exe" "" "$INSTDIR\CPACloud.Launcher.exe" 0
  CreateShortcut "$SMPROGRAMS\CPA Cloud\Uninstall CPA Cloud.lnk" "$INSTDIR\Uninstall.exe"
  Delete "$DESKTOP\CPA Cloud.lnk"
SectionEnd

Section /o "Desktop shortcut" DesktopSection
  SetShellVarContext current
  CreateShortcut "$DESKTOP\CPA Cloud.lnk" "$INSTDIR\CPACloud.Launcher.exe" "" "$INSTDIR\CPACloud.Launcher.exe" 0
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${CoreSection} "$(CoreDescription)"
  !insertmacro MUI_DESCRIPTION_TEXT ${DesktopSection} "$(DesktopDescription)"
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "Uninstall"
  SetShellVarContext current
  Call un.CheckLauncherNotRunning
  Push "$INSTDIR\.cpa-cloud-install"
  Call un.CheckPathAndAncestorsNotReparse
  Push "$INSTDIR\Uninstall.exe"
  Call un.CheckPathAndAncestorsNotReparse
  !insertmacro CheckPayloadPathsNotReparse un.CheckPathAndAncestorsNotReparse
  Delete "$DESKTOP\CPA Cloud.lnk"
  Delete "$SMPROGRAMS\CPA Cloud\CPA Cloud.lnk"
  Delete "$SMPROGRAMS\CPA Cloud\Uninstall CPA Cloud.lnk"
  RMDir "$SMPROGRAMS\CPA Cloud"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  DeleteRegKey HKCU "Software\CPACloud"

  ; Delete only the exact files that the producing build placed in the payload.
  ; Directories are removed non-recursively and therefore remain if anything else
  ; is present. User data at %LOCALAPPDATA%\CPACloud\data is outside this tree.
  !insertmacro RemovePayloadFiles
  Delete "$INSTDIR\.cpa-cloud-install"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
SectionEnd
