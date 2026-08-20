#!/bin/sh
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/terran-installer-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
fake="$tmp/fake-bin"
mkdir "$fake"

cat > "$fake/curl" <<'EOF'
#!/bin/sh
set -eu
out=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then
    out=$2
    shift 2
  else
    shift
  fi
done
case "$out" in
  */SHA256SUMS)
    set -- "$(dirname "$out")"/terran_*.tar.gz
    printf '%064d  %s\n' 0 "$(basename "$1")" > "$out"
    ;;
  *) printf '%s\n' archive > "$out" ;;
esac
EOF

cat > "$fake/shasum" <<'EOF'
#!/bin/sh
printf '%064d  %s\n' 0 "$3"
EOF

cat > "$fake/tar" <<'EOF'
#!/bin/sh
set -eu
destination=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-C" ]; then
    destination=$2
    shift 2
  else
    shift
  fi
done
printf '%s\n' binary > "$destination/terran"
EOF
chmod 0755 "$fake/curl" "$fake/shasum" "$fake/tar"

run_reject() {
  destination=$1
  if PATH="$fake:/usr/bin:/bin" sh "$root/install.sh" v0.1.0 "$destination" >"$tmp/stdout" 2>"$tmp/stderr"; then
    printf '%s\n' "installer accepted unsafe leaf: $destination" >&2
    exit 1
  fi
}

for kind in symlink directory fifo hardlink unsafe-mode; do
  destination="$tmp/$kind"
  mkdir "$destination"
  case "$kind" in
    symlink)
      printf '%s\n' original > "$destination/target"
      ln -s "$destination/target" "$destination/terran"
      ;;
    directory) mkdir "$destination/terran" ;;
    fifo) mkfifo "$destination/terran" ;;
    hardlink)
      printf '%s\n' original > "$destination/terran"
      ln "$destination/terran" "$destination/other-link"
      ;;
    unsafe-mode)
      printf '%s\n' original > "$destination/terran"
      chmod 0777 "$destination/terran"
      ;;
  esac
  run_reject "$destination"
done

destination="$tmp/regular"
mkdir "$destination"
printf '%s\n' old > "$destination/terran"
chmod 0755 "$destination/terran"
PATH="$fake:/usr/bin:/bin" sh "$root/install.sh" v0.1.0 "$destination" >/dev/null
[ "$(cat "$destination/terran")" = binary ]
