#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  case "$arg" in --operation|--file|--known) echo 'Uninstall accepts only --serial and --reboot (or --help).' >&2; exit 2;; esac
done
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec bash "$base/install.sh" --operation uninstall "$@"
