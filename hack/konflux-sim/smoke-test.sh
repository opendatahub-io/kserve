#!/usr/bin/env bash
#
# Smoke-test an image the Konflux simulation just built.
#
#   smoke-test.sh '<kind>:<arg>' <image> '<entrypoint-json>'
#
# Compiling is not the same as starting. Every failure this script looks for
# has shipped before: a binary that links but panics on a missing scheme
# registration, a Python image that builds but cannot import its own server
# package, an ENTRYPOINT that points at a binary that is no longer there.

set -euo pipefail

spec="${1:?usage: smoke-test.sh '<kind>:<arg>' <image> '<entrypoint-json>'}"
image="${2:?usage: smoke-test.sh '<kind>:<arg>' <image> '<entrypoint-json>'}"
expected_entrypoint="${3:?usage: smoke-test.sh '<kind>:<arg>' <image> '<entrypoint-json>'}"

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

configured="$(docker image inspect --format '{{json .Config.Entrypoint}}' "$image")"
if ! jq -e -n --argjson actual "$configured" --argjson expected "$expected_entrypoint" \
  '$actual == $expected' >/dev/null; then
  fail "$image has ENTRYPOINT $configured, expected $expected_entrypoint"
fi

# Every image here is consumed by a restricted SCC. An image that silently goes
# back to running as root still builds and still starts locally. The group is a
# separate field: USER 0:1000 and USER root:1000 are both root.
user="$(docker image inspect --format '{{.Config.User}}' "$image")"
uid="${user%%:*}"
case "$uid" in
  ""|0|root)
    fail "$image runs as '${user:-root}'; the deployed workloads run under a restricted SCC"
    ;;
esac
info "runs as ${user}"

case "$kind" in
  help)
    # Run through the image's own ENTRYPOINT, never --entrypoint: replacing it
    # would hide a Dockerfile whose ENTRYPOINT points at a binary that is not
    # in the image.
    set +e
    out="$(docker run --rm "$image" --help 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -ne 0 ]; then
      printf '%s\n' "$out" >&2
      fail "${expected_entrypoint} --help exited $rc"
    fi
    case "$out" in
      *Usage*) ;;
      *)
        printf '%s\n' "$out" >&2
        fail "${expected_entrypoint} --help printed no usage; the entrypoint started something else"
        ;;
    esac
    info "${expected_entrypoint} --help exited 0 and printed flags"
    ;;

  entrypoint-usage)
    # The storage initializer prints usage and exits 1 when it has no
    # arguments. Reaching that message through the image's own ENTRYPOINT
    # proves the interpreter and the kserve_storage import both work.
    set +e
    out="$(docker run --rm "$image" 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -ne 1 ]; then
      printf '%s\n' "$out" >&2
      fail "${expected_entrypoint} with no arguments exited $rc, expected the usage exit of 1"
    fi
    case "$out" in
      *"Usage: initializer-entrypoint"*) ;;
      *)
        printf '%s\n' "$out" >&2
        fail "${expected_entrypoint} did not reach its usage message; the import failed"
        ;;
    esac
    info "${expected_entrypoint} reached its usage message"
    ;;

  python-module)
    # The only case that overrides the ENTRYPOINT: starting the server would
    # block. The ENTRYPOINT itself was asserted above.
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
