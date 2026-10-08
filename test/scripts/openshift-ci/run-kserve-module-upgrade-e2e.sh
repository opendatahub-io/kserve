#!/usr/bin/env bash
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# OpenShift CI entrypoint for kserve-module N->N+1 upgrade e2e tests.
#
# Intended for a dedicated Prow job (e2e-kserve-module-upgrade) alongside the
# existing e2e-kserve-module sanity job. Unlike other kserve OCP CI jobs, this
# script builds the N (base) module-controller image in the test pod because
# ci-operator only produces the PR (N+1) image.
#
# Required environment (set by Prow / ci-operator):
#   PULL_BASE_SHA                  merge-base / target-branch SHA for image N
#   PULL_PULL_SHA                  PR HEAD SHA for manifests and tests
#   KSERVE_MODULE_CONTROLLER_IMAGE   ci-operator-built module controller (N+1)
#
# Optional:
#   KSERVE_MODULE_BASE_IMAGE         Pre-published pullable ref for N (skips build/push)
#   REGISTRY_NAMESPACE               Namespace for the published N image (default: opendatahub)
#
# Operand images (KSERVE_CONTROLLER_IMAGE, LLMISVC_CONTROLLER_IMAGE, etc.) are
# passed through to setup-cluster.sh unchanged when present.
#
# Local example:
#   export PULL_BASE_SHA="$(git merge-base HEAD origin/master)"
#   export PULL_PULL_SHA="$(git rev-parse HEAD)"
#   export KSERVE_MODULE_CONTROLLER_IMAGE=quay.io/you/kserve-module-controller:dev
#   make e2e-kserve-module-upgrade-ocp

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

readonly PLATFORM="${PLATFORM:-ocp}"
readonly BASE_IMAGE_LOCAL="${KSERVE_MODULE_BASE_IMAGE_LOCAL:-kserve-module-controller:e2e-base}"
readonly DOCKERFILE="${PROJECT_ROOT}/kserve-module-controller.Dockerfile"

BASE_IMAGE_REF=""
ORIGINAL_GIT_REF=""

log() {
  echo "[km-upgrade-ocp] $*"
}

die() {
  echo "[km-upgrade-ocp] ERROR: $*" >&2
  exit 1
}

select_builder() {
  if command -v podman &>/dev/null; then
    echo podman
  elif command -v docker &>/dev/null; then
    echo docker
  else
    die "podman or docker is required to build the base (N) module controller image"
  fi
}

require_env() {
  if [[ -z "${PULL_BASE_SHA:-}" ]]; then
    die "PULL_BASE_SHA is required (merge-base SHA for image N)"
  fi
  if [[ -z "${PULL_PULL_SHA:-}" ]]; then
    die "PULL_PULL_SHA is required (PR HEAD SHA for manifests and tests)"
  fi
  if [[ -z "${KSERVE_MODULE_CONTROLLER_IMAGE:-}" ]]; then
    die "KSERVE_MODULE_CONTROLLER_IMAGE is required (ci-operator N+1 image)"
  fi
  if [[ ! -f "${DOCKERFILE}" ]]; then
    die "Dockerfile not found: ${DOCKERFILE}"
  fi
}

ensure_test_deps() {
  if python3 -m pytest --version >/dev/null 2>&1; then
    return
  fi

  log "Installing test dependencies from python/kserve uv lockfile"
  "${PROJECT_ROOT}/hack/setup/cli/install-uv.sh"
  export PATH="${PROJECT_ROOT}/bin:${PATH}"

  local venv="${PROJECT_ROOT}/.venv-km-upgrade-e2e"
  if [[ ! -d "${venv}" ]]; then
    uv venv "${venv}"
  fi
  # shellcheck disable=SC1091
  source "${venv}/bin/activate"
  pushd "${PROJECT_ROOT}/python/kserve" >/dev/null
  uv sync --active --group test
  popd
}

setup_oc_cli() {
  if command -v oc &>/dev/null && [[ -n "${CLI_DIR:-}" ]]; then
    pushd "${CLI_DIR}" >/dev/null
    if [[ ! -e kubectl ]]; then
      ln -sf oc kubectl
    fi
    if [[ ! -x kustomize ]]; then
      cat > kustomize <<'WRAP'
#!/bin/sh
exec oc kustomize "$@"
WRAP
      chmod +x kustomize
    fi
    popd >/dev/null
    export PATH="${CLI_DIR}:${PATH}"
  fi

  export PATH="${PROJECT_ROOT}/bin:${PATH}"
  if [[ -x "${PROJECT_ROOT}/hack/setup/cli/install-kustomize.sh" ]]; then
    "${PROJECT_ROOT}/hack/setup/cli/install-kustomize.sh"
  fi
}

capture_git_ref() {
  ORIGINAL_GIT_REF="$(git -C "${PROJECT_ROOT}" symbolic-ref --quiet --short HEAD 2>/dev/null \
    || git -C "${PROJECT_ROOT}" rev-parse HEAD)"
}

restore_git_ref() {
  if [[ -z "${ORIGINAL_GIT_REF}" ]]; then
    return
  fi
  git -C "${PROJECT_ROOT}" checkout "${ORIGINAL_GIT_REF}" >/dev/null 2>&1 || true
}

collect_debug_logs() {
  local artifact_dir="${ARTIFACT_DIR:-/tmp}"
  local out_dir="${artifact_dir}/km-upgrade-debug"
  log "Collecting debug logs in ${out_dir}"
  mkdir -p "${out_dir}"

  if ! command -v oc &>/dev/null; then
    return
  fi

  oc logs deployment/kserve-module-controller-manager -n opendatahub --tail=200 \
    >"${out_dir}/controller.log" 2>&1 || true
  oc get kserve -A -o yaml >"${out_dir}/kserve-cr.yaml" 2>&1 || true
  oc get configmap km-upgrade-baseline -n km-upgrade-e2e -o yaml \
    >"${out_dir}/baseline.yaml" 2>&1 || true
  oc logs km-upgrade-probe -n km-upgrade-e2e --tail=50 \
    >"${out_dir}/probe.log" 2>&1 || true
  oc get pods -n opendatahub -o wide >"${out_dir}/pods.txt" 2>&1 || true
  oc get events -n opendatahub --sort-by='.lastTimestamp' | tail -30 \
    >"${out_dir}/events.txt" 2>&1 || true
}

on_error() {
  local exit_code=$?
  log "Upgrade e2e failed (exit ${exit_code}); gathering debug artifacts"
  collect_debug_logs
  exit "${exit_code}"
}

ensure_image_available() {
  local builder="$1"
  local image="$2"

  if "$builder" image inspect "${image}" >/dev/null 2>&1; then
    return
  fi

  log "Image ${image} not in local store; pulling"
  "$builder" pull "${image}"
}

verify_images_differ() {
  local builder="$1"
  local base_id next_id

  ensure_image_available "${builder}" "${KSERVE_MODULE_CONTROLLER_IMAGE}"
  ensure_image_available "${builder}" "${BASE_IMAGE_REF}"

  base_id="$("$builder" image inspect --format='{{.Id}}' "${BASE_IMAGE_REF}")"
  next_id="$("$builder" image inspect --format='{{.Id}}' "${KSERVE_MODULE_CONTROLLER_IMAGE}")"

  log "Base (N) image id: ${base_id}"
  log "Next (N+1) image id: ${next_id}"

  if [[ "${base_id}" == "${next_id}" ]]; then
    die "Base and next module controller images are identical; upgrade test would be meaningless"
  fi
}

resolve_registry_namespace() {
  echo "${REGISTRY_NAMESPACE:-${OPENSHIFT_CI_NAMESPACE:-opendatahub}}"
}

resolve_registry_host() {
  if [[ -n "${KSERVE_MODULE_BASE_IMAGE_REGISTRY:-}" ]]; then
    echo "${KSERVE_MODULE_BASE_IMAGE_REGISTRY}"
    return
  fi
  if ! command -v oc &>/dev/null; then
    die "oc is required to publish the base image to a cluster-pullable registry"
  fi
  oc registry info --internal 2>/dev/null || oc registry info 2>/dev/null \
    || die "could not determine OpenShift integrated registry hostname"
}

publish_base_image() {
  local builder="$1"
  local local_tag="$2"
  local registry ns tag published

  registry="$(resolve_registry_host)"
  ns="$(resolve_registry_namespace)"
  tag="e2e-base-${PULL_BASE_SHA:0:12}"
  published="${registry}/${ns}/kserve-module-controller:${tag}"

  log "Ensuring namespace ${ns} exists for image publish"
  oc get ns "${ns}" >/dev/null 2>&1 || oc create ns "${ns}"

  log "Publishing ${local_tag} -> ${published}"
  oc registry login
  "${builder}" tag "${local_tag}" "${published}"
  if ! "${builder}" push --tls-verify=false "${published}"; then
    die "failed to push base image to ${published} (publish must run from a host that can reach the cluster registry)"
  fi

  BASE_IMAGE_REF="${published}"
  log "Base image published at ${BASE_IMAGE_REF}"
}

build_base_image() {
  local builder="$1"

  log "Checking out base ref ${PULL_BASE_SHA} to build N module controller image"
  git -C "${PROJECT_ROOT}" checkout --detach "${PULL_BASE_SHA}"

  log "Building ${BASE_IMAGE_LOCAL} from ${PULL_BASE_SHA}"
  "${builder}" build -f "${DOCKERFILE}" -t "${BASE_IMAGE_LOCAL}" "${PROJECT_ROOT}"
}

prepare_base_image() {
  local builder="$1"

  if [[ -n "${KSERVE_MODULE_BASE_IMAGE:-}" && "${KSERVE_MODULE_BASE_IMAGE}" == */* ]]; then
    BASE_IMAGE_REF="${KSERVE_MODULE_BASE_IMAGE}"
    log "Using pre-published base image ${BASE_IMAGE_REF}"
    return
  fi

  build_base_image "${builder}"
  publish_base_image "${builder}" "${BASE_IMAGE_LOCAL}"
}

checkout_base_tree() {
  log "Checking out base tree ${PULL_BASE_SHA} for initial setup manifests"
  git -C "${PROJECT_ROOT}" checkout --detach "${PULL_BASE_SHA}"
}

checkout_pr_tree() {
  log "Checking out PR tree ${PULL_PULL_SHA} for upgrade tests"
  git -C "${PROJECT_ROOT}" checkout --detach "${PULL_PULL_SHA}"
}

run_upgrade_flow() {
  export KSERVE_MODULE_UPGRADE_IMAGE="${KSERVE_MODULE_CONTROLLER_IMAGE}"

  checkout_base_tree
  make e2e-setup-kserve-module PLATFORM="${PLATFORM}" E2E_IMG="${BASE_IMAGE_REF}"

  checkout_pr_tree
  make e2e-kserve-module PYTEST_ARGS='-m pre_upgrade --pre-upgrade'

  make e2e-roll-kserve-module PLATFORM="${PLATFORM}" E2E_IMG="${KSERVE_MODULE_CONTROLLER_IMAGE}"

  make e2e-kserve-module PYTEST_ARGS='-m post_upgrade --post-upgrade'
}

main() {
  require_env
  ensure_test_deps
  setup_oc_cli
  capture_git_ref
  trap restore_git_ref EXIT

  trap on_error ERR

  local builder
  builder="$(select_builder)"

  cd "${PROJECT_ROOT}"

  prepare_base_image "${builder}"
  verify_images_differ "${builder}"
  run_upgrade_flow

  trap - ERR
  log "Module upgrade e2e completed successfully"
}

main "$@"
