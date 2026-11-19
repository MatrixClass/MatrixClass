#!/usr/bin/env sh
set -eu

channel_id=${CHANNEL_ID:-}
teacher_address=${TEACHER_ADDRESS:-}
port=${PORT:-}
output_file=${1:-}
run() { command -v "$1" >/dev/null 2>&1 && "$@" || printf '%s unavailable\n' "$1"; }
report() {
  printf '%s\ntimestamp_utc=%s\nos=%s\narch=%s\nchannel_id=%s\n' 'MatrixClass macOS network diagnostic' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$(sw_vers -productVersion 2>/dev/null || uname -s)" "$(uname -m)" "$channel_id"
  printf '%s\n' '--- interfaces ---'; run ifconfig
  printf '%s\n' '--- routes ---'; run netstat -rn
  printf '%s\n' '--- DNS ---'; run scutil --dns
  printf '%s\n' '--- active services ---'; run networksetup -listallhardwareports
  if [ -n "$teacher_address" ]; then
    printf '%s\n' '--- teacher reachability ---'; run ping -c 2 "$teacher_address"
    if [ -n "$port" ] && command -v nc >/dev/null 2>&1; then nc -G 3 -vz "$teacher_address" "$port" 2>&1 || true; fi
  fi
  printf '%s\n' '--- firewall ---'
  if [ -x /usr/libexec/ApplicationFirewall/socketfilterfw ]; then /usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate || true; else printf '%s\n' 'socketfilterfw unavailable'; fi
}
if [ -n "$output_file" ]; then report > "$output_file"; printf 'Network diagnostic written to %s\n' "$output_file"; else report; fi
