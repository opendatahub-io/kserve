#!/bin/bash
# Collect manifests for kserve-module-operator (local development).
# Reads build/manifests-config.yaml and gathers manifests into opt/manifests/.
#
# For production builds, Konflux prefetch-manifests task handles this
# via the same manifests-config.yaml.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
source "${SCRIPT_DIR}/hack/setup/common.sh"

CONFIG_FILE="${MANIFEST_CONFIG:-build/manifests-config.yaml}"
DEST_BASE="opt/manifests"

if ! command -v yq &>/dev/null; then
  "${SCRIPT_DIR}/hack/setup/cli/install-yq.sh"
fi

rm -rf "${DEST_BASE}"

for component in $(yq e '.map | keys | .[]' "${CONFIG_FILE}"); do
  src=$(yq e ".map.\"${component}\".src" "${CONFIG_FILE}")
  dest=$(yq e ".map.\"${component}\".dest" "${CONFIG_FILE}")
  local_flag=$(yq e ".map.\"${component}\".local // false" "${CONFIG_FILE}")
  dest_dir="${DEST_BASE}/${dest}"
  mkdir -p "${dest_dir}"

  if [[ "${local_flag}" == "true" ]]; then
    echo "Collecting ${component} from local ${src}/"
    cp -r "${src}/." "${dest_dir}/"
    rm -rf "${dest_dir}/kserve-module"
  else
    git_url=$(yq e ".map.\"${component}\".\"git.url\"" "${CONFIG_FILE}")
    branch=$(yq e ".map.\"${component}\".branch" "${CONFIG_FILE}")

    echo "Collecting ${component} from ${git_url}@${branch}"
    tmpdir=$(mktemp -d)
    trap 'rm -rf '"${tmpdir}"'' EXIT

    if git clone --depth 1 --branch "${branch}" "${git_url}" "${tmpdir}/repo" 2>/dev/null; then
      cp -r "${tmpdir}/repo/${src}/." "${dest_dir}/"
    else
      git clone "${git_url}" "${tmpdir}/repo"
      git -C "${tmpdir}/repo" checkout "${branch}"
      cp -r "${tmpdir}/repo/${src}/." "${dest_dir}/"
    fi
    rm -rf "${tmpdir}"
    trap - EXIT
  fi

  echo "  ${component}: $(find "${dest_dir}" -type f | wc -l) files"
done

echo "Done."
