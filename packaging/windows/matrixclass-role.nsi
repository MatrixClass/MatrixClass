Unicode True
ManifestDPIAware True
RequestExecutionLevel admin
!include LogicLib.nsh
!include MUI2.nsh

!ifndef APP_VERSION
  !define APP_VERSION "0.1.0"
!endif
!ifndef APP_ARCH
  !define APP_ARCH "amd64"
!endif
!ifndef APP_ROLE
  !define APP_ROLE "teacher"
!endif
!ifndef STAGE_DIR
  !error "STAGE_DIR is required"
!endif
!ifndef OUT_FILE
  !error "OUT_FILE is required"
!endif

!if "${APP_ROLE}" == "student"
  !define APP_LABEL "学生端"
  !define APP_EXECUTABLE "matrixclass-student.exe"
  !define APP_ICON "matrixclass-student.ico"
  !define APP_REGKEY "MatrixClassStudent"
  !define APP_INSTALL_DIR "$PROGRAMFILES64\MatrixClass\Student"
  !define MUI_ICON "${STAGE_DIR}\matrixclass-student.ico"
  !define MUI_UNICON "${STAGE_DIR}\matrixclass-student.ico"
!else
  !define APP_LABEL "教师端"
  !define APP_EXECUTABLE "matrixclass-teacher.exe"
  !define APP_ICON "matrixclass-teacher.ico"
  !define APP_REGKEY "MatrixClassTeacher"
  !define APP_INSTALL_DIR "$PROGRAMFILES64\MatrixClass\Teacher"
  !define MUI_ICON "${STAGE_DIR}\matrixclass-teacher.ico"
  !define MUI_UNICON "${STAGE_DIR}\matrixclass-teacher.ico"
!endif

!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_UNPAGE_FINISH
!insertmacro MUI_LANGUAGE "SimpChinese"

Name "矩联电子教室 MatrixClass ${APP_LABEL}"
OutFile "${OUT_FILE}"
InstallDir "${APP_INSTALL_DIR}"
InstallDirRegKey HKLM "Software\${APP_REGKEY}" "InstallDir"
ShowInstDetails show
ShowUnInstDetails show

VIProductVersion "${APP_VERSION}.0"
VIAddVersionKey "ProductName" "矩联电子教室 MatrixClass ${APP_LABEL}"
VIAddVersionKey "CompanyName" "矩联电子教室 MatrixClass"
VIAddVersionKey "FileDescription" "矩联电子教室 MatrixClass ${APP_LABEL}安装程序"
VIAddVersionKey "FileVersion" "${APP_VERSION}"
VIAddVersionKey "ProductVersion" "${APP_VERSION}"

!if "${APP_ROLE}" == "student"
Function un.onInit
  IfFileExists "$INSTDIR\matrixclass-student.exe" 0 done
  ExecWait '"$INSTDIR\matrixclass-student.exe" --verify-uninstall' $0
  ${If} $0 != 0
    MessageBox MB_ICONSTOP|MB_OK "学生端退出密码验证失败，已取消卸载。"
    Abort
  ${EndIf}
done:
FunctionEnd
!endif

Section "矩联电子教室 MatrixClass ${APP_LABEL} ${APP_ARCH}"
  SetOutPath "$INSTDIR"
  SetOverwrite on
  File "${STAGE_DIR}\${APP_EXECUTABLE}"
  File "${STAGE_DIR}\${APP_ICON}"
  File "${STAGE_DIR}\matrixclass-${APP_ROLE}.png"
  File "${STAGE_DIR}\README.md"

  WriteRegStr HKLM "Software\${APP_REGKEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_REGKEY}" "DisplayName" "矩联电子教室 MatrixClass ${APP_LABEL}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_REGKEY}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_REGKEY}" "Publisher" "矩联电子教室 MatrixClass"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_REGKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_REGKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_REGKEY}" "NoModify" 1
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_REGKEY}" "NoRepair" 1

  CreateDirectory "$SMPROGRAMS\MatrixClass"
  CreateShortCut "$SMPROGRAMS\MatrixClass\MatrixClass ${APP_LABEL}.lnk" "$INSTDIR\${APP_EXECUTABLE}" "" "$INSTDIR\${APP_ICON}"
  CreateShortCut "$DESKTOP\MatrixClass ${APP_LABEL}.lnk" "$INSTDIR\${APP_EXECUTABLE}" "" "$INSTDIR\${APP_ICON}"
  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

Section "Uninstall"
  Delete "$DESKTOP\MatrixClass ${APP_LABEL}.lnk"
  Delete "$SMPROGRAMS\MatrixClass\MatrixClass ${APP_LABEL}.lnk"
  RMDir "$SMPROGRAMS\MatrixClass"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_REGKEY}"
  DeleteRegKey HKLM "Software\${APP_REGKEY}"
  RMDir /r "$INSTDIR"
SectionEnd
