#!/bin/sh
set -eu

usage() {
  printf '%s\n' "usage: $0 vMAJOR.MINOR.PATCH [destination-directory]" >&2
  exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
version=$1
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) usage ;;
esac
if ! printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  usage
fi
plain_version=${version#v}
dest=${2:-"${HOME:?HOME must be set}/.local/bin"}
case "$dest" in
  /*) ;;
  *) printf '%s\n' "destination must be absolute" >&2; exit 2 ;;
esac

validate_binary_leaf() {
  leaf=$1
  if [ -L "$leaf" ]; then
    printf '%s\n' "existing binary path must not be a symlink" >&2
    return 1
  fi
  if [ -e "$leaf" ]; then
    [ -f "$leaf" ] || { printf '%s\n' "existing binary path must be a regular file" >&2; return 1; }
    case "$os" in
      darwin) leaf_meta=$(stat -f '%u %Lp %l' "$leaf") ;;
      linux) leaf_meta=$(stat -c '%u %a %h' "$leaf") ;;
    esac
    leaf_owner=${leaf_meta%% *}
    leaf_rest=${leaf_meta#* }
    leaf_mode=${leaf_rest%% *}
    leaf_links=${leaf_rest#* }
    [ "$leaf_owner" = "$(id -u)" ] || { printf '%s\n' "existing binary must be owned by the effective user" >&2; return 1; }
    [ "$leaf_links" -eq 1 ] || { printf '%s\n' "existing binary must have a single hard link" >&2; return 1; }
    if [ $((0$leaf_mode & 022)) -ne 0 ]; then
      printf '%s\n' "existing binary must not be group- or world-writable" >&2
      return 1
    fi
  fi
}

os=$(uname -s)
case "$os" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) printf '%s\n' "unsupported operating system: $os" >&2; exit 1 ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) printf '%s\n' "unsupported architecture: $arch" >&2; exit 1 ;;
esac

archive="terran_${plain_version}_${os}_${arch}.tar.gz"
base="https://github.com/sean35mm/terran/releases/download/${version}"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/terran.XXXXXX")
install_tmp=
trap 'rm -rf "$tmp"; [ -z "$install_tmp" ] || rm -f "$install_tmp"' EXIT HUP INT TERM

curl --fail --location --proto '=https' --tlsv1.2 --output "$tmp/$archive" "$base/$archive"
curl --fail --location --proto '=https' --tlsv1.2 --output "$tmp/SHA256SUMS" "$base/SHA256SUMS"

expected=
count=0
while IFS=' ' read -r sum file extra; do
  if [ "$file" = "$archive" ] || [ "$file" = "*$archive" ]; then
    [ -z "${extra:-}" ] || { printf '%s\n' "invalid checksum line" >&2; exit 1; }
    expected=$sum
    count=$((count + 1))
  fi
done < "$tmp/SHA256SUMS"
[ "$count" -eq 1 ] && [ "${#expected}" -eq 64 ] || { printf '%s\n' "missing or duplicate exact checksum entry" >&2; exit 1; }
case "$expected" in *[!0-9a-fA-F]*) printf '%s\n' "invalid checksum" >&2; exit 1 ;; esac

if command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$archive")
elif command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive")
else
  printf '%s\n' "no SHA-256 tool found" >&2
  exit 1
fi
actual=${actual%% *}
[ "$actual" = "$expected" ] || { printf '%s\n' "checksum mismatch" >&2; exit 1; }

tar -xzf "$tmp/$archive" -C "$tmp" terran
mkdir -p "$dest"
[ -d "$dest" ] && [ ! -L "$dest" ] || { printf '%s\n' "destination must be a real directory" >&2; exit 1; }
case "$os" in
  darwin) dest_meta=$(stat -f '%u %Lp' "$dest") ;;
  linux) dest_meta=$(stat -c '%u %a' "$dest") ;;
esac
dest_owner=${dest_meta%% *}
dest_mode=${dest_meta#* }
[ "$dest_owner" = "$(id -u)" ] || { printf '%s\n' "destination must be owned by the effective user" >&2; exit 1; }
if [ $((0$dest_mode & 022)) -ne 0 ]; then
  printf '%s\n' "destination must not be group- or world-writable" >&2
  exit 1
fi
install_tmp=$(mktemp "$dest/.terran.install.XXXXXX")
cp "$tmp/terran" "$install_tmp"
chmod 0755 "$install_tmp"
validate_binary_leaf "$dest/terran"
mv -f "$install_tmp" "$dest/terran"
install_tmp=
printf '%s\n' "installed Terran $plain_version to $dest/terran"
