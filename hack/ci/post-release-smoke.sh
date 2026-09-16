#!/usr/bin/env bash
# Post-ODH-release smoke: fresh OCP install from published operator image + post_release pytest.
# Used by OpenShift CI tag postsubmit and local release validation.
set -euo pipefail

resolve_release_tag() {
  if [[ -n "${RELEASE_TAG:-}" ]]; then
    return 0
  fi
  if [[ "${JOB_TYPE:-}" == "postsubmit" && -n "${PULL_BASE_REF:-}" ]]; then
    RELEASE_TAG="${PULL_BASE_REF#refs/tags/}"
    export RELEASE_TAG
    return 0
  fi
  return 1
}

if ! resolve_release_tag; then
  echo "RELEASE_TAG is required (e.g. export RELEASE_TAG=odh-v3.6)"
  echo "OpenShift CI tag postsubmit sets it from PULL_BASE_REF automatically."
  exit 1
fi

OPERATOR_IMAGE="${E2E_IMG:-quay.io/opendatahub/odh-kserve-module-operator:${RELEASE_TAG}}"
export E2E_IMG="${OPERATOR_IMAGE}"

echo "Post-release smoke: tag=${RELEASE_TAG} operator=${OPERATOR_IMAGE}"

if [[ -d .git ]]; then
  git fetch --tags origin
  git checkout "${RELEASE_TAG}"
fi

pip install pytest pyyaml

make e2e-setup-kserve-module PLATFORM=ocp E2E_IMG="${OPERATOR_IMAGE}"
make e2e-kserve-module-post-release
