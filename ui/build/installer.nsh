; Custom NSIS hooks for the xmesh installer and uninstaller.
; Process selection uses executable paths in this installation. Environment variables carry
; literal paths to PowerShell, including apostrophes and other valid Windows path characters.

; $0: 0 = matching process, 1 = clear, 2 or plugin error = inspection failed.
; $1 contains the installer process ID.
!macro XMESH_FIND_IN_INSTDIR
  nsExec::Exec `"$PowerShellPath" -NoProfile -C "$$ErrorActionPreference = 'Stop'; try { $$d = $$env:XMESH_INSTALLER_DIR.TrimEnd('\') + '\'; if (Get-CimInstance -ClassName Win32_Process | ? {$$_.ExecutablePath -and $$_.ProcessId -ne [int]$$env:XMESH_INSTALLER_PID -and $$_.ExecutablePath.StartsWith($$d, [System.StringComparison]::OrdinalIgnoreCase)}) { exit 0 } else { exit 1 } } catch { exit 2 }"`
  Pop $0
!macroend

!macro customCheckAppRunning
  !insertmacro IS_POWERSHELL_AVAILABLE
  Push $1
  System::Call 'kernel32::GetCurrentProcessId()i.r1'
  System::Call 'kernel32::SetEnvironmentVariableW(w "XMESH_INSTALLER_DIR", w "$INSTDIR")i'
  System::Call 'kernel32::SetEnvironmentVariableW(w "XMESH_INSTALLER_PID", w "$1")i'
  ${if} $IsPowerShellAvailable != 0
    MessageBox MB_OK|MB_ICONSTOP "PowerShell is required to verify the running xmesh processes. Enable PowerShell and run setup again." /SD IDOK
    SetErrorLevel 2
    Quit
  ${else}
    !insertmacro XMESH_FIND_IN_INSTDIR
    ${if} $0 == 0
      DetailPrint "Closing xmesh from this installation..."
      nsExec::Exec `"$PowerShellPath" -NoProfile -C "$$ErrorActionPreference = 'Stop'; try { $$d = $$env:XMESH_INSTALLER_DIR.TrimEnd('\') + '\'; Get-CimInstance -ClassName Win32_Process | ? {$$_.ExecutablePath -and $$_.ProcessId -ne [int]$$env:XMESH_INSTALLER_PID -and $$_.ExecutablePath.StartsWith($$d, [System.StringComparison]::OrdinalIgnoreCase)} | % { Stop-Process -Id $$_.ProcessId -Force -ErrorAction SilentlyContinue }; exit 0 } catch { exit 2 }"`
      Pop $0
      ${if} $0 != 0
        MessageBox MB_OK|MB_ICONSTOP "The xmesh process check failed. Resolve Windows process access and run setup again." /SD IDOK
        SetErrorLevel 2
        Quit
      ${endIf}
      Sleep 1000
      !insertmacro XMESH_FIND_IN_INSTDIR
    ${endIf}
    ${if} $0 == 0
      MessageBox MB_OK|MB_ICONSTOP "xmesh from this folder is still running. Quit it and run setup again." /SD IDOK
      SetErrorLevel 2
      Quit
    ${elseIf} $0 != 1
      MessageBox MB_OK|MB_ICONSTOP "The xmesh process check failed. Resolve Windows process access and run setup again." /SD IDOK
      SetErrorLevel 2
      Quit
    ${endIf}
  ${endIf}
  Pop $1
!macroend

; electron-builder retains the old installation directory after uninstallOldVersion --updated.
; The new kernel relocates this user's task actions using the original complete task XML.
!macro customInstall
  ${if} $installationDir != ""
  ${andIf} $installationDir != $INSTDIR
    nsExec::Exec `"$INSTDIR\resources\bin\xmesh.exe" uninstall --dir "$installationDir" --relocate-to "$INSTDIR" --json`
    Pop $0
    ${if} $0 != 0
      MessageBox MB_OK|MB_ICONSTOP "xmesh is installed, and its login tasks need migration. Run the installed resources\bin\xmesh.exe with uninstall --dir <previous folder> --relocate-to <current folder> --json, then start the nodes." /SD IDOK
      SetErrorLevel 2
      Abort
    ${endIf}
  ${endIf}
!macroend

; Task cleanup completes before electron-builder deletes application files. Upgrades preserve
; the tasks for the new installation and customInstall handles installation directory changes.
!macro customUnInstall
  ${IfNot} ${isUpdated}
    ${if} ${FileExists} "$INSTDIR\resources\bin\xmesh.exe"
      nsExec::Exec `"$INSTDIR\resources\bin\xmesh.exe" uninstall --dir "$INSTDIR" --json`
      Pop $0
      ${if} $0 != 0
        MessageBox MB_OK|MB_ICONSTOP "xmesh login task cleanup failed. Resolve the Task Scheduler error and run uninstall again. Application files are retained." /SD IDOK
        SetErrorLevel 2
        Abort
      ${endIf}
    ${else}
      MessageBox MB_OK|MB_ICONSTOP "Restore resources\bin\xmesh.exe by installing xmesh into this folder, then run uninstall again to clean up its login tasks." /SD IDOK
      SetErrorLevel 2
      Abort
    ${endIf}
  ${EndIf}
!macroend
