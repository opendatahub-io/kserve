#!/usr/bin/env bash
#
# Smoke-test an image the Konflux simulation just built.
#
#   smoke-test.sh '<kind>:<arg>' <image>
#
# Compiling is not the same as starting. Every failure this script looks for
# has shipped before: a binary that links but panics on a missing scheme
# registration, a Python image that builds but cannot import its own server
# package, an entrypoint that lost its interpreter.

set -euo pipefail

spec="${1:?usage: smoke-test.sh '<kind>:<arg>' <image>}"
image="${2:?usage: smoke-test.sh '<kind>:<arg>' <image>}"

kind="${spec%%:*}"
arg=""
if [ "$kind" != "$spec" ]; then
  arg="${spec#*:}"
fi

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

info() {
  echo "  $*"
}

# Every image here is consumed by a restricted SCC. An image that silently goes
# back to running as root still builds and still starts locally.
user="$(docker image inspect --format '{{.Config.User}}' "$image")"
case "$user" in
  ""|0|root|0:0|root:root)
    fail "$image runs as '${user:-root}'; the deployed workloads run under a restricted SCC"
    ;;
esac
info "runs as ${user}"

case "$kind" in
  help)
    # --help is the cheapest way to prove the binary links, initialises and
    # exits. A panic in an init() or a missing scheme registration shows up
    # here as a non-zero exit.
    set +e
    out="$(docker run --rm --entrypoint "$arg" "$image" --help 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -ne 0 ]; then
      printf '%s\n' "$out" >&2
      fail "$arg --help exited $rc"
    fi
    case "$out" in
      *Usage*) ;;
      *)
        printf '%s\n' "$out" >&2
        fail "$arg --help printed no flags; the entrypoint started something else"
        ;;
    esac
    info "$arg --help exited 0 and printed flags"
    ;;

  entrypoint-usage)
    # The storage initializer prints usage and exits 1 when it has no
    # arguments. Reaching that message proves the interpreter and the
    # kserve_storage import both work.
    set +e
    out="$(docker run --rm --entrypoint "$arg" "$image" 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -ne 1 ]; then
      printf '%s\n' "$out" >&2
      fail "$arg with no arguments exited $rc, expected the usage exit of 1"
    fi
    case "$out" in
      *"Usage: initializer-entrypoint"*) ;;
      *)
        printf '%s\n' "$out" >&2
        fail "$arg did not reach its usage message; the import failed"
        ;;
    esac
    info "$arg reached its usage message"
    ;;

  python-module)
    out="$(docker run --rm --entrypoint python "$image" -c "import ${arg}" 2>&1)" || {
      printf '%s\n' "$out" >&2
      fail "python could not import ${arg}"
    }
    info "python imports ${arg}"
    ;;

  *)
    fail "unknown smoke kind '${kind}'"
    ;;
esac
