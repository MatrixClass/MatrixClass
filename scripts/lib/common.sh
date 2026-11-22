# Shared helpers for MatrixClass build scripts (POSIX sh). Source with:
#   . "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/lib/common.sh"

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
build_tags=${BUILD_TAGS:-mygo_nowebview}

# Targets with a native mygo UI backend. Compile-only targets build, but mygo
# returns "mygo: <os>/<arch> is not supported yet" at startup.
supported_windows="amd64 arm64"
supported_linux="amd64 arm64"
supported_darwin="amd64 arm64"
compile_only_windows="386"
compile_only_linux="loong64 riscv64 386 arm"
compile_only_darwin=""

mc_version() {
  v=${1:-${MATRIXCLASS_VERSION:-${VERSION:-0.1.0}}}
  printf '%s\n' "${v#v}"
}

abs_path() {
  case "$1" in
    /*|[A-Za-z]:*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "$repo_root" "$1" ;;
  esac
}

contains_word() {
  needle=$1; shift
  for w in $*; do [ "$w" = "$needle" ] && return 0; done
  return 1
}

assert_arches() {
  goos=$1; shift
  eval "sup=\$supported_$goos; co=\$compile_only_$goos"
  for a in "$@"; do
    if contains_word "$a" $sup; then continue; fi
    if contains_word "$a" $co; then
      printf 'WARNING: %s/%s is compile-only: mygo has no native UI backend, the app exits with "not supported yet". Not for release.\n' "$goos" "$a" >&2
      continue
    fi
    printf 'Unsupported %s architecture: %s (supported: %s; compile-only: %s)\n' "$goos" "$a" "$sup" "$co" >&2
    return 1
  done
}

# go_build <goos> <goarch> <app> <output> <version> [extra ldflags]
# -X main.version is a no-op until cmd/<app> declares `var version string`.
go_build() {
  goos=$1 goarch=$2 app=$3 out=$4 ver=$5
  shift 5
  goarm=
  [ "$goarch" = arm ] && goarm=7
  printf '  go build %s/%s %s -> %s\n' "$goos" "$goarch" "$app" "$out"
  (cd "$repo_root" && env GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" CGO_ENABLED=0 \
    go build -trimpath -tags "$build_tags" -ldflags "-s -w -X main.version=$ver $*" -o "$out" "./cmd/$app")
}

# host_tool <name>: builds tools/<name> for the host (cached by the Go build
# cache, so repeated calls are cheap) and prints its path.
host_tool() {
  tool_dir="$repo_root/dist/.tools"
  exe=$1
  case "$(uname -s 2>/dev/null)" in MINGW*|MSYS*|CYGWIN*) exe="$1.exe" ;; esac
  mkdir -p "$tool_dir"
  (cd "$repo_root" && env -u GOOS -u GOARCH -u GOARM CGO_ENABLED=0 go build -trimpath -o "$tool_dir/$exe" "./tools/$1") >&2 || return 1
  printf '%s\n' "$tool_dir/$exe"
}

# make_archive <output .zip|.tar.gz> <base dir> <entry>...
make_archive() {
  out=$1 base=$2
  shift 2
  tool=$(host_tool mkarchive) || return 1
  "$tool" -o "$out" -C "$base" "$@"
  printf '  archive %s\n' "$out"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# write_sha256sums <dir> [file name]
write_sha256sums() {
  dir=$1 name=${2:-SHA256SUMS}
  tmp="$dir/.$name.tmp"
  : > "$tmp"
  for f in $(cd "$dir" && LC_ALL=C ls -1); do
    [ -f "$dir/$f" ] || continue
    [ "$f" = "$name" ] && continue
    printf '%s  %s\n' "$(sha256_of "$dir/$f")" "$f" >> "$tmp"
  done
  mv "$tmp" "$dir/$name"
  printf '  checksums %s\n' "$dir/$name"
}
