#!/usr/bin/env bash
# Post-ODH-release smoke: fresh OCP install from published operator image + post_release pytest.
# Used by OpenShift CI optional /test e2e-kserve-module-post-release and local validation.
set -euo pipefail

# Newest odh-vX.Y tag (no -ea / -rc suffix). Prefers version sort.
latest_odh_release_tag() {
  git fetch --tags origin >/dev/null 2>&1 || true
  git tag -l 'odh-v*' \
    | grep -E '^odh-v[0-9]+\.[0-9]+$' \
    | sort -t. -k1,1 -k2,2n -k3,3n \
    | tail -n1
}

resolve_release_tag() {
  if [[ -n "${RELEASE_TAG:-}" ]]; then
    return 0
  fi
  # Legacy: tag postsubmit would pass the pushed ref (not used; prowgen cannot
  # emit tag-scoped postsubmits under *-master-postsubmits.yaml).
  if [[ "${JOB_TYPE:-}" == "postsubmit" && -n "${PULL_BASE_REF:-}" ]]; then
    RELEASE_TAG="${PULL_BASE_REF#refs/tags/}"
    export RELEASE_TAG
    return 0
  fi
  local latest
  latest="$(latest_odh_release_tag || true)"
  if [[ -n "${latest}" ]]; then
    RELEASE_TAG="${latest}"
    export RELEASE_TAG
    echo "RELEASE_TAG unset; using latest odh-vX.Y tag: ${RELEASE_TAG}"
    return 0
  fi
  return 1
}

if ! resolve_release_tag; then
  echo "RELEASE_TAG is required (e.g. export RELEASE_TAG=odh-v3.6)"
  echo "In OpenShift CI, /test e2e-kserve-module-post-release uses the newest odh-vX.Y tag when unset."
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
