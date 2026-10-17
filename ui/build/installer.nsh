; Custom NSIS hooks for the xmesh installer and uninstaller (electron-builder includes build/installer.nsh).
;
; The default check ends running processes by image name (xmesh.exe), and every xmesh node from
; any folder runs as xmesh.exe, so a test node in another folder gets killed too. This replacement
; stops only the processes whose executable lies under this installation's directory, and never
; kills by image name. Without PowerShell the processes cannot be told apart, so none are stopped.

; Sets $0 to 0 when an xmesh process runs from $INSTDIR, other than the installer itself.
; $1 holds the installer's own process id (set by the caller).
!macro XMESH_FIND_IN_INSTDIR
  nsExec::Exec `"$PowerShellPath" -NoProfile -C "$$d = '$INSTDIR'.TrimEnd('\') + '\'; if (Get-CimInstance -ClassName Win32_Process | ? {$$_.ExecutablePath -and $$_.ProcessId -ne $1 -and $$_.ExecutablePath.StartsWith($$d, 'CurrentCultureIgnoreCase')}) { exit 0 } else { exit 1 }"`
  Pop $0
!macroend

!macro customCheckAppRunning
  !insertmacro IS_POWERSHELL_AVAILABLE
  Push $1
  System::Call 'kernel32::GetCurrentProcessId()i.r1'
  ${if} $IsPowerShellAvailable != 0
    MessageBox MB_OKCANCEL|MB_ICONEXCLAMATION "xmesh may still be running from this folder. Quit it from the tray, then click OK to continue." /SD IDOK IDOK +2
    Quit
  ${else}
    !insertmacro XMESH_FIND_IN_INSTDIR
    ${if} $0 == 0
      DetailPrint "Closing xmesh from this installation..."
      nsExec::Exec `"$PowerShellPath" -NoProfile -C "$$d = '$INSTDIR'.TrimEnd('\') + '\'; Get-CimInstance -ClassName Win32_Process | ? {$$_.ExecutablePath -and $$_.ProcessId -ne $1 -and $$_.ExecutablePath.StartsWith($$d, 'CurrentCultureIgnoreCase')} | % { Stop-Process -Id $$_.ProcessId -Force -ErrorAction SilentlyContinue }"`
      Pop $0
      # Give the files time to be released before the installer touches them.
      Sleep 1000
      !insertmacro XMESH_FIND_IN_INSTDIR
      ${if} $0 == 0
        MessageBox MB_OK|MB_ICONSTOP "xmesh from this folder could not be closed. Quit it and run the installer again." /SD IDOK
        Quit
      ${endIf}
    ${endIf}
  ${endIf}
  Pop $1
!macroend

; The logon tasks this user registered for xmesh nodes run from this folder. Remove the ones that
; do before the files go, or Task Scheduler restarts a kernel from a folder being deleted. Tasks
; that run from other folders (a test node, say) stay. An upgrade runs the old uninstaller too;
; it keeps the tasks, since the new version starts from them.
!macro customUnInstall
  ${IfNot} ${isUpdated}
    ${if} ${FileExists} "$INSTDIR\resources\bin\xmesh.exe"
      nsExec::Exec `"$INSTDIR\resources\bin\xmesh.exe" uninstall --dir "$INSTDIR" --json`
      Pop $0
    ${endIf}
  ${EndIf}
!macroend
