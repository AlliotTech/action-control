#!/usr/bin/env bash
set -euo pipefail

base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
operation=install
serial=${ANDROID_SERIAL:-}
reboot=false
file= known=
usage() {
  cat <<'USAGE'
Action Control — macOS / Linux (ADB required)
  ./install.sh [--serial SERIAL] [--reboot]
  ./install.sh --operation update [--serial SERIAL] [--reboot]
  ./install.sh --operation import-config [--file CONFIG.json] [--known KNOWN.json] [--serial SERIAL]
  ./uninstall.sh --reboot [--serial SERIAL]
Use an extracted official release, including payload/ and SHA256SUMS.
Uninstall restores the recorded WiFi/DNS baseline, not BL/ADB/Factory Mode.
Checksums detect corruption; obtain releases from a trusted publisher.
USAGE
}
while (($#)); do
  case "$1" in
    --operation|--serial|--file|--known)
      (($# >= 2)) || { echo "Missing argument: $1" >&2; exit 2; }
      case "$1" in --operation) operation=$2;; --serial) serial=$2;; --file) file=$2;; --known) known=$2;; esac
      shift 2;;
    --reboot) reboot=true; shift;;
    --help|-h) usage; exit 0;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2;;
  esac
done
case "$operation" in install|update|uninstall|import-config) ;; *) echo 'Invalid operation' >&2; exit 2;; esac
if [[ "$operation" == uninstall && "$reboot" != true ]]; then echo 'Uninstall requires --reboot; nothing changed.' >&2; exit 2; fi
if [[ "$operation" != import-config && -n "$file$known" ]]; then echo 'Import paths require --operation import-config' >&2; exit 2; fi
if [[ "$operation" == import-config && -z "$file$known" ]]; then echo 'Select at least one configuration import' >&2; exit 2; fi
for input in "$file" "$known"; do [[ -z "$input" || -f "$input" ]] || { echo "Missing import file: $input" >&2; exit 2; }; done
command -v adb >/dev/null || { echo 'Install Android platform-tools (adb) first.' >&2; exit 1; }
[[ -f "$base/payload/action-control" && -f "$base/payload/manifest.json" && -f "$base/SHA256SUMS" ]] || { echo 'Run this from an extracted complete release.' >&2; exit 1; }
if command -v sha256sum >/dev/null; then hash=(sha256sum); else command -v shasum >/dev/null; hash=(shasum -a 256); fi
while read -r expected name; do
  [[ -n "$name" && "$name" != /* && "$name" != ../* && "$name" != */../* && "$name" != *\\* && -f "$base/$name" && ! -L "$base/$name" ]] || { echo 'Invalid checksum path' >&2; exit 1; }
  actual=$("${hash[@]}" "$base/$name"); actual=${actual%% *}
  [[ "$actual" == "$expected" ]] || { echo "Release checksum mismatch: $name" >&2; exit 1; }
done < "$base/SHA256SUMS"

if [[ -z "$serial" ]]; then
  devices=$(adb devices)
  selected=()
  while read -r candidate state rest; do [[ "$state" != device ]] || selected+=("$candidate"); done <<< "$devices"
  ((${#selected[@]} == 1)) || { echo 'Select exactly one online device with --serial SERIAL.' >&2; exit 1; }
  serial=${selected[0]}
fi
adb_device() { adb -s "$serial" "$@"; }
[[ $(adb_device get-state) == device ]] || { echo 'Selected device is not online.' >&2; exit 1; }
[[ $(adb_device shell id -u | tr -d '\r') == 0 ]] || { echo 'Root ADB is required.' >&2; exit 1; }
[[ $(adb_device shell uname -m | tr -d '\r') == aarch64 ]] || { echo 'Expected ARM64 camera.' >&2; exit 1; }
boot=$(adb_device shell cat /proc/sys/kernel/random/boot_id | tr -d '\r')
[[ "$boot" =~ ^[a-f0-9-]{36}$ ]] || { echo 'Cannot read camera boot identity.' >&2; exit 1; }
stage= forward= complete=false
finish() {
  local code=$?
  trap - EXIT
  if [[ "$complete" != true ]]; then
    [[ -z "$forward" ]] || adb_device forward --remove "tcp:$forward" || true
    [[ -z "$stage" ]] || printf 'Operation incomplete. Recovery/staging retained at %s; do not force-delete installation backups.\n' "$stage" >&2
  fi
  exit "$code"
}
trap finish EXIT
stage=$(adb_device shell 'umask 077; mktemp -d /blackbox/.action-control-stage-XXXXXX' | tr -d '\r')
[[ "$stage" =~ ^/blackbox/\.action-control-stage-[a-zA-Z0-9]+$ ]] || { echo 'Invalid exclusive staging path.' >&2; stage=; exit 1; }
adb_device push "$base/payload/action-control" "$stage/bootstrap"
adb_device shell "chmod 700 '$stage/bootstrap'"
args=("$operation")
case "$operation" in
  install|update)
    adb_device push "$base/payload" "$stage/payload"
    args+=(--source "$stage/payload");;
  import-config)
    for key in file known; do
      case "$key" in file) input=$file;; known) input=$known;; esac
      [[ -n "$input" ]] || continue
      adb_device push "$input" "$stage/import-$key"
      args+=("--$key" "$stage/import-$key")
    done;;
esac
[[ "$reboot" != true ]] || args+=(--reboot)
# Every remote argument below is fixed or generated from a restricted staging name.
adb_device shell "$stage/bootstrap" "${args[@]}"
if [[ "$reboot" == true ]]; then
  adb_device reboot
  deadline=$((SECONDS + 150))
  changed=false
  while ((SECONDS < deadline)); do
    if [[ $(adb_device get-state 2>/dev/null || true) == device ]]; then
      next=$(adb_device shell cat /proc/sys/kernel/random/boot_id 2>/dev/null | tr -d '\r' || true)
      if [[ "$next" =~ ^[a-f0-9-]{36}$ && "$next" != "$boot" ]]; then changed=true; break; fi
    fi
    sleep 1
  done
  [[ "$changed" == true ]] || { echo 'Reboot not verified within 150s; native runtime restoration is unconfirmed.' >&2; exit 1; }
fi
if [[ "$operation" == uninstall ]]; then
  adb_device shell "$stage/bootstrap" verify --removed
else
  adb_device shell "$stage/bootstrap" verify
  forward=$(adb_device forward --no-rebind tcp:0 tcp:8080 | tr -d '\r')
  [[ "$forward" =~ ^[0-9]+$ ]] || { echo 'ADB did not return a local forwarded port.' >&2; forward=; exit 1; }
  printf 'Console: http://127.0.0.1:%s\n' "$forward"
  printf 'This ADB forward remains available. Remove it with: adb -s %q forward --remove tcp:%s\n' "$serial" "$forward"
fi
adb_device shell "rm -rf '$stage'"
stage=
complete=true
printf '%s completed and verified for device %s.\n' "$operation" "$serial"
