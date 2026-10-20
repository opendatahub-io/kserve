#!/bin/sh

if [ "$#" -eq 0 ]; then
    echo 'usage: retry <command> [args...]' >&2
    exit 2
fi

for attempt in 1 2 3; do
    printf 'Attempt %s/3: %s\n' "$attempt" "$*"
    if "$@"; then
        exit 0
    fi
    if [ "$attempt" -eq 3 ]; then
        exit 1
    fi
    sleep "$((attempt * 2))"
done
