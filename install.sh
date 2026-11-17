#!/bin/sh
# Installer: downloads the release archive for this machine, verifies its
# SHA-256 against the release's checksums file and installs the binary.
#
#   curl -fsSL https://raw.githubusercontent.com/<repo>/main/install.sh | sh
#
# Environment (the prefix is ENV_PREFIX below):
#   <PREFIX>INSTALL_DIR   directory to install into (default: $PREFIX/bin
#                         if PREFIX is set, else ~/.local/bin)
#   <PREFIX>VERSION       release to install, e.g. v1.2.3 (default: latest)
#   <PREFIX>REPO          GitHub "owner/name" to download from
#
# Needs only: sh, uname, mktemp, tar (unzip on Windows), curl or wget, and
# sha256sum, shasum or openssl.
set -eu

# RENAMING THE TOOL: these three lines are the only place this file names
# it. They mirror Name, Repo and EnvPrefix in internal/appinfo/appinfo.go.
NAME="slackcli"
REPO="maelvls/slackcli"
ENV_PREFIX="SLACKCLI_"

# CONTRACT with .goreleaser.yaml and internal/update: the archive is
# "${NAME}_${os}_${arch}.tar.gz" (.zip on Windows), the binary sits at its
# root, and its SHA-256 is listed in the release asset below.
CHECKSUMS="checksums.txt"

log() { printf '%s\n' "$*"; }
die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

# setting FOO prints the value of ${ENV_PREFIX}FOO, or nothing.
setting() {
  eval "printf '%s' \"\${${ENV_PREFIX}$1:-}\""
}

download() {
  url=$1
  dest=$2
  if have curl; then
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL "$url" -o "$dest"
  elif have wget; then
    wget_https "$url" "$dest"
  else
    die "curl or wget is required"
  fi
}

# wget_https downloads like the curl call above: over https only, redirects
# included. wget has no equivalent of --proto-redir (its --https-only is
# about recursive downloads and lets an https URL redirect to plain http),
# so redirects are followed here, one at a time, and only to https.
wget_https() {
  wget_url=$1
  wget_dest=$2
  wget --help 2>&1 | grep -q -e '--max-redirect' ||
    die "this wget (BusyBox?) cannot be kept from following a redirect to plain http; install curl, or GNU wget"
  wget_hops=0
  while :; do
    case "$wget_url" in
      https://*) ;;
      *) die "refusing to download $wget_url: only https is allowed" ;;
    esac
    # -S prints the response headers on stderr, even with -q.
    if wget_headers=$(wget -q -S --max-redirect=0 -O "$wget_dest" "$wget_url" 2>&1); then
      return 0
    fi
    wget_location=$(printf '%s\n' "$wget_headers" |
      awk 'tolower($1) == "location:" { print $2; exit }' | tr -d '\r')
    [ -n "$wget_location" ] || return 1
    case "$wget_location" in
      /*)
        # Relative to the host that redirected.
        wget_host=${wget_url#https://}
        wget_location="https://${wget_host%%/*}$wget_location"
        ;;
    esac
    wget_hops=$((wget_hops + 1))
    [ "$wget_hops" -le 10 ] || die "too many redirects downloading $1"
    wget_url=$wget_location
  done
}

sha256_of() {
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif have openssl; then
    openssl dgst -sha256 -r "$1" | awk '{print $1}'
  else
    die "sha256sum, shasum or openssl is required to verify the download"
  fi
}

detect_platform() {
  uname_s=$(uname -s)
  case "$uname_s" in
    Linux) os="linux" ;;
    Darwin) os="darwin" ;;
    MSYS* | MINGW* | CYGWIN*) os="windows" ;;
    *) die "unsupported operating system: $uname_s (releases exist for Linux, macOS and Windows). With Go installed, try: go install github.com/$REPO/cmd/$NAME@latest" ;;
  esac

  uname_m=$(uname -m)
  case "$uname_m" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) die "unsupported architecture: $uname_m (releases exist for x86_64 and arm64). With Go installed, try: go install github.com/$REPO/cmd/$NAME@latest" ;;
  esac

  if [ "$os" = "windows" ]; then
    archive_ext="zip"
    exe_suffix=".exe"
  else
    archive_ext="tar.gz"
    exe_suffix=""
  fi
}

main() {
  repo=$(setting REPO)
  repo=${repo:-$REPO}

  install_dir=$(setting INSTALL_DIR)
  if [ -z "$install_dir" ]; then
    if [ -n "${PREFIX:-}" ]; then
      install_dir="$PREFIX/bin"
    elif [ -n "${HOME:-}" ]; then
      install_dir="$HOME/.local/bin"
    else
      die "HOME is not set; set ${ENV_PREFIX}INSTALL_DIR to the directory to install into"
    fi
  fi

  detect_platform
  archive="${NAME}_${os}_${arch}.${archive_ext}"
  binary="${NAME}${exe_suffix}"

  version=$(setting VERSION)
  if [ -n "$version" ]; then
    case "$version" in
      v*) tag="$version" ;;
      *) tag="v$version" ;;
    esac
    base_url="https://github.com/$repo/releases/download/$tag"
    release="release $tag"
  else
    base_url="https://github.com/$repo/releases/latest/download"
    release="the latest release"
  fi

  if [ "$os" = "windows" ]; then
    have unzip || die "unzip is required to unpack $archive"
  else
    have tar || die "tar is required to unpack $archive"
  fi

  # mktemp -d creates a private (0700) directory with an unpredictable name.
  tmp=$(mktemp -d 2>/dev/null || mktemp -d -t "$NAME.XXXXXXXX") || die "cannot create a temporary directory"
  staged=""
  trap 'rm -rf "$tmp"; [ -z "$staged" ] || rm -f "$staged"' EXIT
  trap 'exit 1' HUP INT TERM

  log "Downloading $archive from $release of $repo..."
  download "$base_url/$archive" "$tmp/$archive" ||
    die "could not download $base_url/$archive ($release may not exist, or may have no build for $os/$arch)"
  download "$base_url/$CHECKSUMS" "$tmp/$CHECKSUMS" ||
    die "could not download $base_url/$CHECKSUMS; refusing to install an unverified download"

  # Verification is not optional: there is deliberately no way to skip it.
  expected=$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/$CHECKSUMS")
  [ -n "$expected" ] || die "$CHECKSUMS has no entry for $archive; refusing to install an unverified download"
  actual=$(sha256_of "$tmp/$archive")
  [ -n "$actual" ] || die "could not compute the SHA-256 of $archive"
  [ "$expected" = "$actual" ] || die "checksum mismatch for $archive: expected $expected, got $actual"
  log "Checksum verified."

  # Only the binary is extracted, by name, so the archive cannot write
  # anywhere else.
  mkdir "$tmp/unpacked"
  if [ "$os" = "windows" ]; then
    unzip -q "$tmp/$archive" "$binary" -d "$tmp/unpacked" || die "could not unpack $archive"
  else
    tar -xzf "$tmp/$archive" -C "$tmp/unpacked" "$binary" || die "could not unpack $archive"
  fi
  [ -f "$tmp/unpacked/$binary" ] || die "$archive does not contain $binary"

  mkdir -p "$install_dir" || die "cannot create $install_dir; set ${ENV_PREFIX}INSTALL_DIR to a directory you can write to"
  # Copy next to the destination, then rename: the swap is atomic, and
  # works even while an older version is running.
  staged="$install_dir/.$binary.install.$$"
  cp "$tmp/unpacked/$binary" "$staged" ||
    die "cannot write to $install_dir; set ${ENV_PREFIX}INSTALL_DIR to a directory you can write to"
  chmod 755 "$staged"
  mv -f "$staged" "$install_dir/$binary" || die "cannot replace $install_dir/$binary"
  staged=""

  log "Installed $install_dir/$binary"
  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *) log "Add it to your PATH: export PATH=\"$install_dir:\$PATH\"" ;;
  esac
}

# Everything runs from main, called on the last line, so that a download
# of this script cut short (curl | sh) executes nothing.
main "$@"
