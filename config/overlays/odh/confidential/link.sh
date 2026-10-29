#!/bin/sh
# Sourced before either model download or inference. Never fall back to plain disk.
DATA_DIR="${DATA_DIR:-/mnt/models}"
PID_FILE=/dev/shm/luks-helper.pid
WAITED=0
while [ "$WAITED" -lt 900 ]; do
  if [ -s "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    target="/proc/$PID/root/mnt/storage"
    if [ -e "$target/.ready" ]; then
      if [ -L "$DATA_DIR" ]; then
        [ "$(readlink "$DATA_DIR")" = "$target" ] || { echo "cc-storage: unexpected model link" >&2; exit 1; }
      else
        if [ -d "$DATA_DIR" ]; then
          rmdir "$DATA_DIR" || { echo "cc-storage: model directory must be empty" >&2; exit 1; }
        fi
        ln -s "$target" "$DATA_DIR" || exit 1
      fi
      return 0
    fi
  fi
  sleep 1
  WAITED=$((WAITED + 1))
done
echo "cc-storage: encrypted filesystem did not become ready" >&2
exit 1
