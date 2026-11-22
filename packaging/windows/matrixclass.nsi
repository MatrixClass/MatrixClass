Unicode True
ManifestDPIAware True
RequestExecutionLevel admin

!ifndef APP_VERSION
  !define APP_VERSION "0.1.0"
!endif
!ifndef APP_ARCH
  !define APP_ARCH "amd64"
!endif
!ifndef STAGE_DIR
  !error "STAGE_DIR is required"
!endif
!ifndef OUT_FILE
  !error "OUT_FILE is required"
!endif

Name "MatrixClass"
OutFile "${OUT_FILE}"
InstallDir "$PROGRAMFILES64\MatrixClass"
InstallDirRegKey HKLM "Software\MatrixClass" "InstallDir"
ShowInstDetails show
ShowUnInstDetails show

VIProductVersion "${APP_VERSION}.0"
VIAddVersionKey "ProductName" "MatrixClass"
VIAddVersionKey "CompanyName" "MatrixClass"
VIAddVersionKey "FileDescription" "MatrixClass Windows installer"
VIAddVersionKey "FileVersion" "${APP_VERSION}"
VIAddVersionKey "ProductVersion" "${APP_VERSION}"

Page directory
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

Section "MatrixClass ${APP_ARCH}"
  SetOutPath "$INSTDIR"
  SetOverwrite on
  File /r "${STAGE_DIR}\*"

  WriteRegStr HKLM "Software\MatrixClass" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\MatrixClass" "DisplayName" "MatrixClass"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\MatrixClass" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\MatrixClass" "Publisher" "MatrixClass"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\MatrixClass" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\MatrixClass" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\MatrixClass" "NoModify" 1
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\MatrixClass" "NoRepair" 1

  CreateDirectory "$SMPROGRAMS\MatrixClass"
  CreateShortCut "$SMPROGRAMS\MatrixClass\MatrixClass Teacher.lnk" "$INSTDIR\matrixclass-teacher.exe"
  CreateShortCut "$SMPROGRAMS\MatrixClass\MatrixClass Student.lnk" "$INSTDIR\matrixclass-student.exe"
  CreateShortCut "$DESKTOP\MatrixClass Teacher.lnk" "$INSTDIR\matrixclass-teacher.exe"
  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

Section "Uninstall"
  Delete "$DESKTOP\MatrixClass Teacher.lnk"
  Delete "$SMPROGRAMS\MatrixClass\MatrixClass Teacher.lnk"
  Delete "$SMPROGRAMS\MatrixClass\MatrixClass Student.lnk"
  RMDir "$SMPROGRAMS\MatrixClass"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\MatrixClass"
  DeleteRegKey HKLM "Software\MatrixClass"
  RMDir /r "$INSTDIR"
SectionEnd
