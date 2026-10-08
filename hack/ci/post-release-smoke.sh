#!/usr/bin/env bash
# Post-ODH-release smoke: fresh OCP install from published operator image + post_release pytest.
# Used by OpenShift CI optional /test e2e-kserve-module-post-release and local validation.
set -euo pipefail

ODH_KSERVE_GIT_URL="${ODH_KSERVE_GIT_URL:-https://github.com/opendatahub-io/kserve.git}"

# Prow src checkouts often have no remote named "origin" (or no remotes at all).
# Prefer the first configured remote; otherwise fetch tags from the public HTTPS URL.
fetch_odh_tags() {
  local remote
  remote="$(git remote 2>/dev/null | head -n1 || true)"
  if [[ -n "${remote}" ]]; then
    git fetch --tags "${remote}" "$@"
  else
    git fetch --tags "${ODH_KSERVE_GIT_URL}" "$@"
  fi
}

# Newest odh-vX.Y or odh-vX.Y-(ea|rc)* tag. GNU sort -V order (examples):
#   odh-v3.5 < odh-v3.6 < odh-v3.6-ea1 < odh-v3.6-ea2 < odh-v3.7 < odh-v3.7-ea1
# so an -ea of X.Y ranks above plain X.Y, and the next minor ranks above any -ea of the prior.
latest_odh_release_tag() {
  fetch_odh_tags >/dev/null 2>&1 || true
  git tag -l 'odh-v*' \
    | grep -E '^odh-v[0-9]+\.[0-9]+(-[A-Za-z]+[0-9]+)?$' \
    | sort -V \
    | tail -n1
}

resolve_release_tag() {
  # Local override to pin a specific tag. /test cannot pass a tag; CI leaves
  # RELEASE_TAG unset and uses the newest matching tag below.
  if [[ -n "${RELEASE_TAG:-}" ]]; then
    return 0
  fi
  local latest
  latest="$(latest_odh_release_tag || true)"
  if [[ -n "${latest}" ]]; then
    RELEASE_TAG="${latest}"
    export RELEASE_TAG
    echo "RELEASE_TAG unset; using latest odh-vX.Y / odh-vX.Y-ea|rc tag: ${RELEASE_TAG}"
    return 0
  fi
  return 1
}

checkout_release_tag() {
  local tag="$1"
  if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
    git checkout "${tag}"
    return 0
  fi
  echo "Tag ${tag} not present locally; fetching tags..."
  fetch_odh_tags
  git checkout "${tag}"
}

if ! resolve_release_tag; then
  echo "RELEASE_TAG is required (e.g. export RELEASE_TAG=odh-v3.6 or odh-v3.6-ea2)"
  echo "In OpenShift CI, /test e2e-kserve-module-post-release uses the newest"
  echo "odh-vX.Y or odh-vX.Y-(ea|rc)* tag (version sort; next minor beats prior -ea)."
  echo "Pin locally: export RELEASE_TAG=<tag> && bash hack/ci/post-release-smoke.sh"
  exit 1
fi

OPERATOR_IMAGE="${E2E_IMG:-quay.io/opendatahub/odh-kserve-module-operator:${RELEASE_TAG}}"
export E2E_IMG="${OPERATOR_IMAGE}"

echo "Post-release smoke: tag=${RELEASE_TAG} operator=${OPERATOR_IMAGE}"

if [[ -d .git ]]; then
  checkout_release_tag "${RELEASE_TAG}"
fi

pip install pytest pyyaml

make e2e-setup-kserve-module PLATFORM=ocp E2E_IMG="${OPERATOR_IMAGE}"
make e2e-kserve-module-post-release
