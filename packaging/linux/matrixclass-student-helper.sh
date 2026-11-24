#!/bin/sh
# Helper used by the matrixclass-student .deb/.rpm maintainer scripts.
#   launch  start the student client in every active graphical session
#   verify  ask for the student exit password before uninstalling
#   stop    terminate running student clients
set -u

student_bin=/opt/matrixclass/matrixclass-student
password_rel=.local/share/matrixclass/student-exit-password.sha256

user_home() {
  getent passwd "$1" 2>/dev/null | cut -d: -f6
}

session_env() {
  uid=$1
  key=$2
  for pid in $(pgrep -u "$uid" 2>/dev/null); do
    value=$(tr '\0' '\n' < "/proc/$pid/environ" 2>/dev/null | sed -n "s/^$key=//p" | head -n 1)
    if [ -n "$value" ]; then
      printf '%s\n' "$value"
      return 0
    fi
  done
  return 0
}

graphical_sessions() {
  command -v loginctl >/dev/null 2>&1 || return 0
  loginctl list-sessions --no-legend 2>/dev/null | awk '{print $1}' | while read -r sid; do
    [ -n "$sid" ] || continue
    stype=$(loginctl show-session "$sid" -p Type --value 2>/dev/null)
    state=$(loginctl show-session "$sid" -p State --value 2>/dev/null)
    case "$stype" in x11|wayland) ;; *) continue ;; esac
    [ "$state" = closing ] && continue
    user=$(loginctl show-session "$sid" -p Name --value 2>/dev/null)
    display=$(loginctl show-session "$sid" -p Display --value 2>/dev/null)
    [ -n "$user" ] && printf '%s %s\n' "$user" "${display:-:0}"
  done | sort -u
}

run_as_user() {
  user=$1
  display=$2
  shift 2
  uid=$(id -u "$user" 2>/dev/null) || return 1
  home=$(user_home "$user")
  runtime=/run/user/$uid
  wayland=""
  for sock in "$runtime"/wayland-*; do
    if [ -S "$sock" ]; then
      wayland=$(basename "$sock")
      break
    fi
  done
  xauth=$(session_env "$uid" XAUTHORITY)
  if [ -z "$xauth" ] && [ -f "$home/.Xauthority" ]; then
    xauth="$home/.Xauthority"
  fi
  set -- env HOME="$home" USER="$user" LOGNAME="$user" DISPLAY="$display" \
    XAUTHORITY="$xauth" XDG_RUNTIME_DIR="$runtime" WAYLAND_DISPLAY="$wayland" \
    DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime/bus" "$@"
  if [ "$(id -u)" = "$uid" ]; then
    "$@"
  elif command -v runuser >/dev/null 2>&1; then
    runuser -u "$user" -- "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo -u "$user" -- "$@"
  else
    return 1
  fi
}

do_launch() {
  [ -x "$student_bin" ] || return 0
  graphical_sessions | while read -r user display; do
    uid=$(id -u "$user" 2>/dev/null) || continue
    if pgrep -u "$uid" -f "^$student_bin( |\$)" >/dev/null 2>&1; then
      continue
    fi
    run_as_user "$user" "$display" setsid "$student_bin" </dev/null >/dev/null 2>&1 &
  done
  return 0
}

do_stop() {
  pkill -f "^$student_bin( |\$)" >/dev/null 2>&1 || true
  return 0
}

password_users() {
  getent passwd | while IFS=: read -r name _ uid _ _ home _; do
    [ -n "$home" ] || continue
    if [ "$uid" -ge 1000 ] 2>/dev/null || [ "$uid" = 0 ]; then
      [ -f "$home/$password_rel" ] && printf '%s\n' "$name"
    fi
  done
}

do_verify() {
  [ -x "$student_bin" ] || return 0
  protected=$(password_users)
  [ -n "$protected" ] || return 0
  sessions=$(graphical_sessions)
  for user in $protected; do
    display=$(printf '%s\n' "$sessions" | awk -v u="$user" '$1 == u {print $2; exit}')
    if [ -z "$display" ]; then
      echo "矩联电子教室学生端：用户 $user 已设置退出密码，请在该用户的桌面会话中卸载以完成密码验证" >&2
      return 1
    fi
    if ! run_as_user "$user" "$display" "$student_bin" --verify-uninstall; then
      echo "矩联电子教室学生端：退出密码验证失败，已取消卸载" >&2
      return 1
    fi
  done
  return 0
}

case "${1:-}" in
  launch) do_launch ;;
  stop) do_stop ;;
  verify) do_verify ;;
  *) echo "usage: $0 launch|stop|verify" >&2; exit 2 ;;
esac
