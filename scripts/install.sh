#!/usr/bin/env sh
# Download and install the latest brucli release for macOS or Linux.
set -eu

repository="leodeim/brucli"
install_dir="${INSTALL_DIR:-$HOME/.local/bin}"

fail() {
  echo "$*" >&2
  exit 1
}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "brucli supports macOS and Linux." ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "brucli supports amd64 and arm64 CPUs." ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required to install brucli."
if command -v sha256sum >/dev/null 2>&1; then
  checksum_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  checksum_tool=shasum
else
  fail "sha256sum or shasum is required to verify the download."
fi

download() {
  curl --proto '=https' --tlsv1.2 --retry 3 -fsSL "$@"
}

temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/brucli-install.XXXXXX")
staged_binary=""
cleanup() {
  rm -rf "$temp_dir"
  if [ -n "$staged_binary" ]; then
    rm -f "$staged_binary"
  fi
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

# Resolve the tag once so both assets come from the same release.
release_url=$(download -o /dev/null -w '%{url_effective}' "https://github.com/${repository}/releases/latest")
case "$release_url" in
  "https://github.com/${repository}/releases/tag/"v*) ;;
  *) fail "Could not find the latest brucli release." ;;
esac
tag=${release_url##*/}
download_url="https://github.com/${repository}/releases/download/${tag}"
binary="brucli_${os}_${arch}"

echo "Downloading brucli ${tag} for ${os}/${arch}..."
download "${download_url}/${binary}" -o "${temp_dir}/${binary}"
download "${download_url}/checksums.txt" -o "${temp_dir}/checksums.txt"

expected_checksum=$(awk -v file="$binary" '$2 == file { print $1 }' "${temp_dir}/checksums.txt")
[ -n "$expected_checksum" ] || fail "No checksum found for ${binary}."
if [ "$checksum_tool" = sha256sum ]; then
  actual_checksum=$(sha256sum "${temp_dir}/${binary}")
else
  actual_checksum=$(shasum -a 256 "${temp_dir}/${binary}")
fi
actual_checksum=${actual_checksum%% *}
[ "$actual_checksum" = "$expected_checksum" ] || fail "Checksum verification failed for ${binary}."

mkdir -p "$install_dir" || fail "Cannot create ${install_dir}; set INSTALL_DIR to a writable directory."
[ -w "$install_dir" ] || fail "Cannot write to ${install_dir}; set INSTALL_DIR to a writable directory."
[ ! -d "${install_dir}/brucli" ] || fail "${install_dir}/brucli is a directory."
# Stage beside the destination so replacement is atomic on the same filesystem.
staged_binary=$(mktemp "${install_dir}/.brucli.XXXXXX")
install -m 0755 "${temp_dir}/${binary}" "$staged_binary"
mv -f "$staged_binary" "${install_dir}/brucli"
staged_binary=""

echo "Installed brucli ${tag} to ${install_dir}/brucli"
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *) echo "Add ${install_dir} to PATH in your shell profile to run brucli." ;;
esac
