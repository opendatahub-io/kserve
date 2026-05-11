#!/usr/bin/env bash

set -euo pipefail

BUILDER=${BUILDER:-"docker"}
case "${BUILDER}" in
    docker|podman) ;;
    *) echo "Error: BUILDER must be 'docker' or 'podman', got '${BUILDER}'"; exit 1 ;;
esac
GITHUB_SHA=${GITHUB_SHA:-"master"}

if [ -z "${QUAY_REPO:-}" ]; then
    echo "Error: QUAY_REPO environment variable is not set"
    exit 1
fi
export KO_DOCKER_REPO=$QUAY_REPO/kserve

# Build via Make, tag with :$GITHUB_SHA, and push.
# Usage: build_tag_push <make-target> <ci-image-ref>
#   ci-image-ref: registry/name (no tag); the Make target must produce this same name.
#   Overrides in Makefile.overrides.mk align build names with CI names.
build_tag_push() {
    local target="$1" ci_ref="$2"
    local ci_image="${ci_ref}:${GITHUB_SHA}"
    echo "Building ${ci_ref##*/}..."
    make "$target"
    $BUILDER tag "$ci_ref" "$ci_image"
    $BUILDER push "$ci_image"
    echo "${ci_ref##*/} completed successfully"
}

build_tag_push docker-build-sklearn              "$KO_DOCKER_REPO/sklearnserver"
export SKLEARN_IMAGE=$KO_DOCKER_REPO/sklearnserver:$GITHUB_SHA

build_tag_push docker-build-storageInitializer   "$KO_DOCKER_REPO/kserve-storage-initializer"
export STORAGE_INITIALIZER_IMAGE=$KO_DOCKER_REPO/kserve-storage-initializer:$GITHUB_SHA

build_tag_push docker-build-agent                "$KO_DOCKER_REPO/kserve-agent"
export KSERVE_AGENT_IMAGE=$KO_DOCKER_REPO/kserve-agent:$GITHUB_SHA

build_tag_push docker-build-router               "$KO_DOCKER_REPO/kserve-router"
export KSERVE_ROUTER_IMAGE=$KO_DOCKER_REPO/kserve-router:$GITHUB_SHA

build_tag_push docker-build                      "$KO_DOCKER_REPO/kserve-controller"
export KSERVE_CONTROLLER_IMAGE=$KO_DOCKER_REPO/kserve-controller:$GITHUB_SHA

build_tag_push docker-build-llmisvc              "$KO_DOCKER_REPO/llmisvc-controller"
export LLMISVC_CONTROLLER_IMAGE=$KO_DOCKER_REPO/llmisvc-controller:$GITHUB_SHA

echo "All images built and pushed successfully!"
