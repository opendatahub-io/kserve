#!/usr/bin/env bash
#
# Generate the build matrix for the Konflux build simulation from the
# PipelineRuns in .tekton/.
#
#   generate-build-matrix.sh                  # GitHub Actions matrix on stdout
#   generate-build-matrix.sh --relevant-paths # paths that can change an image
#
# Everything here is derived from .tekton/ and the Dockerfiles, not from a
# hand-maintained list, so a new Konflux target cannot appear without appearing
# in CI. A target that cannot be mapped fails this script instead of being
# dropped: a gate that silently skips the image it does not understand is worse
# than no gate.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

mode="matrix"
case "${1:-}" in
  "") ;;
  --relevant-paths) mode="relevant" ;;
  *)
    echo "unknown argument '${1}'" >&2
    exit 2
    ;;
esac

YQ="${YQ:-./bin/yq}"
case "$YQ" in
  */*) ;;
  *) YQ="$(command -v "$YQ" 2>/dev/null || true)" ;;
esac
if [ -z "$YQ" ] || [ ! -x "$YQ" ]; then
  echo "missing $YQ; run 'make yq' first" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "missing jq" >&2
  exit 1
fi

# Targets .tekton/ builds that this job cannot reproduce, and why. Keep this
# list short and explicit. Anything not listed here must be buildable.
#
# kserve-kueue-controller is a placeholder Dockerfile whose binary prints
# "placeholder image" and exits 1, built from a registry.redhat.io subscription
# base on four architectures. Neither half is reproducible on a GitHub runner.
unbuildable_reason() {
  case "$1" in
    odh-kserve-kueue-controller)
      echo "placeholder image (binary exits 1) built from a registry.redhat.io subscription base; the other three architectures need the Konflux multi-platform controller"
      ;;
  esac
}

# The smoke test proves the image is runnable, not just that it compiled. It is
# chosen per image because a Python server and a controller do not fail the
# same way.
smoke_for() {
  case "$1" in
    kserve-agent) echo "help" ;;
    kserve-router) echo "help" ;;
    kserve-storage-initializer) echo "entrypoint-usage" ;;
    odh-kserve-autogluon-server) echo "python-module:autogluonserver" ;;
    *) echo "help" ;;
  esac
}

# The entrypoint the image is expected to carry. The smoke test compares this
# with the built image's Config.Entrypoint and then starts the image through it:
# overriding it would hide the exact breakage - a Dockerfile whose ENTRYPOINT
# points at a binary that is no longer there - that the test exists to catch.
entrypoint_for() {
  case "$1" in
    kserve-agent) echo '["/ko-app/agent"]' ;;
    kserve-router) echo '["/ko-app/router"]' ;;
    kserve-storage-initializer) echo '["/storage-initializer/scripts/initializer-entrypoint"]' ;;
    odh-kserve-autogluon-server) echo '["python","-m","autogluonserver"]' ;;
    *) echo '["/manager"]' ;;
  esac
}

errors=0
entries=""
relevant=""

fail() {
  echo "ERROR: $*" >&2
  errors=$((errors + 1))
}

# Context-relative COPY sources of a Dockerfile, as matchable prefixes. A
# change to any of them can change the image, so the skip filter has to use
# this rather than a hand-written list: the root Dockerfile copies LICENSE, and
# forgetting that is how a deleted LICENSE skips every build.
copy_sources() {
  local dockerfile="$1" context="$2"
  [ -n "$dockerfile" ] || return 0
  awk '
    /^[[:space:]]*COPY[[:space:]]/ {
      line=$0
      sub(/^[[:space:]]*COPY[[:space:]]+/, "", line)
      if (line ~ /^--from=/) next
      n=split(line, parts, /[[:space:]]+/)
      i=1
      while (i<=n && parts[i] ~ /^--/) i++
      # Every operand except the last is a source.
      for (j=i; j<n; j++) print parts[j]
    }
  ' "$dockerfile" | while IFS= read -r source; do
    [ -n "$source" ] || continue
    source="${source#./}"
    # An ARG-expanded path contributes its literal directory prefix.
    case "$source" in
      *'$'*) source="${source%%\$*}" ;;
    esac
    source="${source%/}"
    [ -n "$source" ] || continue
    if [ "$context" != "." ]; then
      source="$context/$source"
    fi
    # A file matches exactly, a directory matches as a prefix. The repo is the
    # source of truth for which one it is.
    if [ -f "$source" ]; then
      printf '%s\n' "$source"
    else
      printf '%s/\n' "$source"
    fi
  done
}

add_relevant() {
  case " ${relevant} " in
    *" $1 "*) ;;
    *) relevant="${relevant}${relevant:+$'\n'}$1" ;;
  esac
}

for pipeline in "$repo_root"/.tekton/*pull*.yaml; do
  [ -f "$pipeline" ] || continue

  # Only PR builds. The *-push.yaml files in the same directory are not what
  # this workflow stands in for.
  on_cel="$("$YQ" -r '.metadata.annotations["pipelinesascode.tekton.dev/on-cel-expression"] // ""' "$pipeline")"
  case "$on_cel" in
    *pull_request*) ;;
    *) continue ;;
  esac

  source_file="${pipeline#"$repo_root"/}"
  component="$("$YQ" -r '.metadata.labels["appstudio.openshift.io/component"] // ""' "$pipeline")"
  if [ -z "$component" ]; then
    fail "$source_file: no appstudio.openshift.io/component label, cannot name the image"
    continue
  fi
  name="${component%-ci}"

  params="$(mktemp)"
  "$YQ" -o=json '.spec.params // []' "$pipeline" > "$params"

  param() {
    jq -r --arg n "$1" '.[] | select(.name == $n) | .value' "$params" | head -1
  }
  param_list() {
    jq -c --arg n "$1" '[.[] | select(.name == $n) | .value] | first // []' "$params"
  }

  image="$(param output-image)"
  dockerfile_param="$(param dockerfile)"
  context="$(param path-context)"
  build_args="$(param_list build-args)"
  platforms="$(param_list build-platforms)"

  [ -n "$image" ] || fail "$source_file: no output-image"
  [ -n "$dockerfile_param" ] || fail "$source_file: no dockerfile"
  [ -n "$context" ] || context="."
  [ "$platforms" != "[]" ] || platforms='["linux/x86_64"]'

  # Konflux's buildah task resolves the Dockerfile against the build context
  # first and the repository root second. python/ targets rely on this:
  # path-context python + dockerfile storage-initializer.Dockerfile.
  dockerfile=""
  if [ -f "$context/$dockerfile_param" ]; then
    dockerfile="$context/$dockerfile_param"
  elif [ -f "$dockerfile_param" ]; then
    dockerfile="$dockerfile_param"
  else
    fail "$source_file: dockerfile '$dockerfile_param' not found in '$context' or the repository root"
  fi

  if [ -n "$dockerfile" ]; then
    # Every build-arg the PipelineRun passes must be consumed by the Dockerfile
    # and reach the tool that uses it. A Dockerfile that drops GOTAGS still
    # builds, and ships an image with the distro-gated code silently excluded.
    gotags_consumed=false
    grep -Eq "^[[:space:]]*ARG[[:space:]]+GOTAGS([[:space:]]|=|$)" "$dockerfile" && gotags_consumed=true

    while IFS= read -r arg; do
      [ -n "$arg" ] || continue
      key="${arg%%=*}"
      value="${arg#*=}"
      if ! grep -Eq "^[[:space:]]*ARG[[:space:]]+${key}([[:space:]]|=|$)" "$dockerfile"; then
        fail "$source_file: build-arg '$key' is passed but $dockerfile does not declare ARG $key"
        continue
      fi
      if [ "$key" = "GOTAGS" ]; then
        if [ -z "$value" ]; then
          fail "$source_file: GOTAGS is passed empty; the built image would exclude the distro-gated code"
        fi
        # The declaration is not enough: `go build -tags=other` ignores the
        # variable while still reading as if it were consumed.
        if ! grep -Eq 'go[[:space:]]+build.*-tags[= ][^|]*\$\{?GOTAGS' "$dockerfile"; then
          fail "$source_file: $dockerfile declares GOTAGS but does not pass it to 'go build -tags'"
        fi
      fi
    done < <(jq -r '.[]' <<<"$build_args")

    # The mirror gap: a Dockerfile that consumes GOTAGS but a PipelineRun that
    # stopped passing it builds the same silent non-distro image.
    if [ "$gotags_consumed" = "true" ] && ! jq -e 'any(.[]; startswith("GOTAGS="))' <<<"$build_args" >/dev/null; then
      fail "$source_file: $dockerfile consumes GOTAGS but the PipelineRun does not pass GOTAGS=distro"
    fi
  fi

  if [ "$mode" = "relevant" ]; then
    while IFS= read -r path; do
      [ -n "$path" ] && add_relevant "$path"
    done < <(copy_sources "$dockerfile" "$context")
    rm -f "$params"
    continue
  fi

  smoke="$(smoke_for "$name")"
  entrypoint="$(entrypoint_for "$name")"
  buildable=true
  reason="$(unbuildable_reason "$name")"
  if [ -n "$reason" ]; then
    buildable=false
  elif ! jq -e 'index("linux/x86_64") or index("linux/amd64")' <<<"$platforms" >/dev/null; then
    buildable=false
    reason="declares no native platform: $platforms"
  fi

  entry="$(jq -c -n \
    --arg name "$name" \
    --arg image "$image" \
    --arg dockerfile "$dockerfile" \
    --arg context "$context" \
    --argjson build_args "$build_args" \
    --argjson platforms "$platforms" \
    --arg smoke "$smoke" \
    --arg entrypoint "$entrypoint" \
    --arg source "$source_file" \
    --argjson build "$buildable" \
    --arg reason "$reason" \
    '{name: $name, image: $image, dockerfile: $dockerfile, context: $context,
      build_args: $build_args, platforms: $platforms, smoke: $smoke,
      entrypoint: ($entrypoint | fromjson), source: $source,
      build: $build, reason: (if $reason == "" then null else $reason end)}')"
  entries+="$entry"$'\n'
  rm -f "$params"
done

if [ "$errors" -gt 0 ]; then
  echo "FAIL: $errors Konflux target(s) could not be mapped to a build." >&2
  exit 1
fi

if [ "$mode" = "relevant" ]; then
  # Things that change the gate itself, or the build environment, always run.
  printf '%s\n' '.tekton/' '.github/actions/' 'hack/konflux-sim/' '.dockerignore' \
    '.github/workflows/konflux-build-simulation.yml'
  printf '%s\n' "${relevant}" | sort -u
  exit 0
fi

if [ -z "$entries" ]; then
  echo "FAIL: no pull-request PipelineRuns found in .tekton/." >&2
  exit 1
fi

jq -c -n --argjson include "$(printf '%s' "$entries" | jq -s -c '.')" '{include: $include}'
