#!/usr/bin/env sh
set -eu

student_app=${1:-/Applications/MatrixClass Student.app}
student_bin="$student_app/Contents/MacOS/matrixclass-student"

if [ ! -x "$student_bin" ]; then
  echo "未找到学生端应用: $student_app" >&2
  exit 1
fi

if ! "$student_bin" --verify-uninstall; then
  echo "学生端退出密码验证失败，已取消卸载" >&2
  exit 1
fi

rm -rf "$student_app"
echo "MatrixClass Student 已卸载，用户数据已保留"
