#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
TMP_DIR="$(mktemp -d -t "kserve-manifests-test.XXXXXXXXXX")"
trap 'rm -rf -- "$TMP_DIR"' EXIT

FAKE_GITHUB="${TMP_DIR}/github"
MODELEXPRESS_REPO="${FAKE_GITHUB}/test/modelexpress"
MODELCONTROLLER_REPO="${FAKE_GITHUB}/test/modelcontroller"
OUTPUT_DIR="${TMP_DIR}/output"

mkdir -p "${MODELEXPRESS_REPO}/config/manifests"
git -C "${MODELEXPRESS_REPO}" init -q
git -C "${MODELEXPRESS_REPO}" config user.name test
git -C "${MODELEXPRESS_REPO}" config user.email test@example.com
printf 'modelexpress\n' > "${MODELEXPRESS_REPO}/config/manifests/marker.txt"
git -C "${MODELEXPRESS_REPO}" add .
git -C "${MODELEXPRESS_REPO}" -c core.hooksPath=/dev/null commit -qm 'add test manifests'
git -C "${MODELEXPRESS_REPO}" branch -M main

mkdir -p "${MODELCONTROLLER_REPO}/config/server"
git -C "${MODELCONTROLLER_REPO}" init -q
git -C "${MODELCONTROLLER_REPO}" config user.name test
git -C "${MODELCONTROLLER_REPO}" config user.email test@example.com
printf 'modelcontroller override\n' > "${MODELCONTROLLER_REPO}/config/server/clusterrole.yaml"
git -C "${MODELCONTROLLER_REPO}" add .
git -C "${MODELCONTROLLER_REPO}" -c core.hooksPath=/dev/null commit -qm 'add test manifests'
git -C "${MODELCONTROLLER_REPO}" branch -M main

mkdir -p "${OUTPUT_DIR}/modelcontroller"
printf 'stale\n' > "${OUTPUT_DIR}/modelcontroller/stale.yaml"

GITHUB_URL="${FAKE_GITHUB}" bash "${SCRIPT_DIR}/get_kserve_manifests.sh" \
    "${OUTPUT_DIR}" \
    --modelexpress=test:modelexpress:main:config/manifests

test -f "${OUTPUT_DIR}/modelcontroller/.source.yaml"
test -f "${OUTPUT_DIR}/modelcontroller/server/clusterrole.yaml"
test ! -e "${OUTPUT_DIR}/modelcontroller/stale.yaml"
test -f "${OUTPUT_DIR}/modelexpress/marker.txt"

OVERRIDE_OUTPUT_DIR="${TMP_DIR}/override-output"
GITHUB_URL="${FAKE_GITHUB}" bash "${SCRIPT_DIR}/get_kserve_manifests.sh" \
    "${OVERRIDE_OUTPUT_DIR}" \
    --modelcontroller=test:modelcontroller:main:config \
    --modelexpress=test:modelexpress:main:config/manifests

grep -qx 'modelcontroller override' "${OVERRIDE_OUTPUT_DIR}/modelcontroller/server/clusterrole.yaml"
test ! -e "${OVERRIDE_OUTPUT_DIR}/modelcontroller/.source.yaml"

INVALID_PREFETCHED_DIR="${TMP_DIR}/invalid-prefetched"
mkdir -p "${INVALID_PREFETCHED_DIR}/modelcontroller"
printf 'revision: short\n' > "${INVALID_PREFETCHED_DIR}/modelcontroller/.source.yaml"
if PREFETCHED_MANIFESTS_DIR="${INVALID_PREFETCHED_DIR}" \
    GITHUB_URL="${FAKE_GITHUB}" \
    bash "${SCRIPT_DIR}/get_kserve_manifests.sh" \
        "${TMP_DIR}/invalid-output" \
        --modelexpress=test:modelexpress:main:config/manifests; then
    echo "ERROR: Invalid prefetched revision was accepted" >&2
    exit 1
fi

echo "Manifest collection test passed."
