Unicode True
ManifestDPIAware True
RequestExecutionLevel admin
!include LogicLib.nsh

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

Function un.onInit
  IfFileExists "$INSTDIR\matrixclass-student.exe" 0 done
  ExecWait '"$INSTDIR\matrixclass-student.exe" --verify-uninstall' $0
  ${If} $0 != 0
    MessageBox MB_ICONSTOP|MB_OK "学生端退出密码验证失败，已取消卸载。"
    Abort
  ${EndIf}
done:
FunctionEnd

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
  CreateShortCut "$SMPROGRAMS\MatrixClass\MatrixClass Teacher.lnk" "$INSTDIR\matrixclass-teacher.exe" "" "$INSTDIR\matrixclass-teacher.ico"
  CreateShortCut "$SMPROGRAMS\MatrixClass\MatrixClass Student.lnk" "$INSTDIR\matrixclass-student.exe" "" "$INSTDIR\matrixclass-student.ico"
  CreateShortCut "$DESKTOP\MatrixClass Teacher.lnk" "$INSTDIR\matrixclass-teacher.exe" "" "$INSTDIR\matrixclass-teacher.ico"
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
