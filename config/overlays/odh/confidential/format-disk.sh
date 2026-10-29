#!/bin/bash
# All key handling and disk operations run inside the attested guest.
set -euo pipefail
DEVICE=/dev/block-device
case "${LUKS_RESOURCE_ID:-}" in
  kbs:///*/*/*) resource_path=${LUKS_RESOURCE_ID#kbs:///} ;;
  *) echo "cc-storage: LUKS_RESOURCE_ID must be a KBS resource reference" >&2; exit 1 ;;
esac
fetch_pass() {
  exec 3<>/dev/tcp/127.0.0.1/8006 || return 1
  printf 'GET /cdh/resource/%s HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n' "$resource_path" >&3
  local status line body
  IFS= read -r status <&3 || { exec 3<&- 3>&-; return 1; }
  while IFS= read -r line <&3; do
    line=${line%$'\r'}
    [ -z "$line" ] && break
  done
  body=$(cat <&3)
  exec 3<&- 3>&-
  case "$status" in *' 200 '*) ;; *) return 1 ;; esac
  case "$body" in ""|sealed.*) return 1 ;; esac
  printf '%s' "$body"
}
PASS=""
for ((attempt=0; attempt<90; attempt++)); do
  if PASS=$(fetch_pass); then break; fi
  sleep 2
done
[ -n "$PASS" ] || { echo "cc-storage: could not obtain LUKS passphrase from CDH" >&2; exit 1; }
export PASS
if cryptsetup isLuks "$DEVICE"; then
  printf '%s' "$PASS" | cryptsetup --key-file=- luksOpen --test-passphrase "$DEVICE" || {
    echo "cc-storage: LUKS key mismatch; existing volume left intact" >&2
    exit 1
  }
elif blkid -p "$DEVICE" >/dev/null 2>&1; then
  echo "cc-storage: refusing to format a device with an existing signature" >&2
  exit 1
fi
exec /usr/local/bin/luks-helper format-disk
