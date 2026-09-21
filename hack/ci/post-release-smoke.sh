#!/usr/bin/env bash
# Post-ODH-release smoke: fresh OCP install from published operator image + post_release pytest.
# Used by OpenShift CI optional /test e2e-kserve-module-post-release and local validation.
set -euo pipefail

# Newest plain odh-vX.Y tag (no -ea / -rc / other suffix). Prefers version sort.
latest_odh_release_tag() {
  git fetch --tags origin >/dev/null 2>&1 || true
  git tag -l 'odh-v*' \
    | grep -E '^odh-v[0-9]+\.[0-9]+$' \
    | sort -t. -k1,1 -k2,2n -k3,3n \
    | tail -n1
}

# Pre-release tags like odh-v3.6-ea1 (for hints when auto-resolve fails).
list_odh_prerelease_tags() {
  git tag -l 'odh-v*' | grep -E '^odh-v[0-9]+\.[0-9]+-' || true
}

resolve_release_tag() {
  # Local override (required for -ea/-rc tags). /test cannot pass a tag; CI leaves
  # RELEASE_TAG unset and uses the newest plain odh-vX.Y tag below.
  if [[ -n "${RELEASE_TAG:-}" ]]; then
    return 0
  fi
  local latest
  latest="$(latest_odh_release_tag || true)"
  if [[ -n "${latest}" ]]; then
    RELEASE_TAG="${latest}"
    export RELEASE_TAG
    echo "RELEASE_TAG unset; using latest plain odh-vX.Y tag: ${RELEASE_TAG}"
    local prerelease
    prerelease="$(list_odh_prerelease_tags | tail -n1 || true)"
    if [[ -n "${prerelease}" ]]; then
      echo "Note: pre-release tags exist (e.g. ${prerelease}). /test ignores -ea/-rc suffixes."
      echo "To validate those, run the same script with: export RELEASE_TAG=${prerelease}"
    fi
    return 0
  fi
  return 1
}

if ! resolve_release_tag; then
  echo "RELEASE_TAG is required (e.g. export RELEASE_TAG=odh-v3.6)"
  echo "In OpenShift CI, /test e2e-kserve-module-post-release uses the newest plain odh-vX.Y tag only."
  prerelease="$(list_odh_prerelease_tags | tail -n3 || true)"
  if [[ -n "${prerelease}" ]]; then
    echo "Found pre-release tags (not selected automatically):"
    echo "${prerelease}"
    echo "Same flow, pinned tag: export RELEASE_TAG=<tag> && bash hack/ci/post-release-smoke.sh"
  fi
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
