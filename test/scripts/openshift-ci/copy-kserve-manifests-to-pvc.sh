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
# This script copies KServe manifests from the PR branch into the ODH operator's PVC

set -eu

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
PROJECT_ROOT="${SCRIPT_DIR}/../../../"

: "${OPERATOR_NAMESPACE:=openshift-operators}"
: "${KSERVE_MANIFESTS_PVC:=kserve-custom-manifests}"

# Image environment variables. When QUAY_REPO is set, prefer locally-built images
# (tagged :${GITHUB_SHA:-master}) over the upstream defaults. This mirrors the logic
# in deploy.kserve-manual.sh and covers both BUILD_KSERVE_IMAGES=true (where
# build-kserve-images.sh exports these vars) and BUILD_KSERVE_IMAGES=false / standalone
# setup-kserve-ocp invocations (where the vars are not yet set but images are pushed).
if [[ -n "${QUAY_REPO:-}" ]]; then
  _repo="${QUAY_REPO}/kserve"
  _tag="${GITHUB_SHA:-master}"
  : "${KSERVE_CONTROLLER_IMAGE:=${_repo}/kserve-controller:${_tag}}"
  : "${KSERVE_AGENT_IMAGE:=${_repo}/kserve-agent:${_tag}}"
  : "${KSERVE_ROUTER_IMAGE:=${_repo}/kserve-router:${_tag}}"
  : "${STORAGE_INITIALIZER_IMAGE:=${_repo}/kserve-storage-initializer:${_tag}}"
  : "${LLMISVC_CONTROLLER_IMAGE:=${_repo}/llmisvc-controller:${_tag}}"
else
  : "${KSERVE_CONTROLLER_IMAGE:=quay.io/opendatahub/kserve-controller:latest}"
  : "${KSERVE_AGENT_IMAGE:=quay.io/opendatahub/kserve-agent:latest}"
  : "${KSERVE_ROUTER_IMAGE:=quay.io/opendatahub/kserve-router:latest}"
  : "${STORAGE_INITIALIZER_IMAGE:=quay.io/opendatahub/kserve-storage-initializer:latest}"
  : "${LLMISVC_CONTROLLER_IMAGE:=quay.io/opendatahub/llmisvc-controller:latest}"
fi
: "${ODH_MODEL_CONTROLLER_IMAGE:=quay.io/opendatahub/odh-model-controller:fast}"
: "${ODH_MODEL_CONTROLLER_REF:=incubating}"

echo "Copying manifests from current branch to ODH operator PVC..."
echo "Using PR images:"
echo "  KSERVE_CONTROLLER_IMAGE=$KSERVE_CONTROLLER_IMAGE"
echo "  LLMISVC_CONTROLLER_IMAGE=$LLMISVC_CONTROLLER_IMAGE"
echo "  KSERVE_AGENT_IMAGE=$KSERVE_AGENT_IMAGE"
echo "  KSERVE_ROUTER_IMAGE=$KSERVE_ROUTER_IMAGE"
echo "  STORAGE_INITIALIZER_IMAGE=$STORAGE_INITIALIZER_IMAGE"
echo "  ODH_MODEL_CONTROLLER_IMAGE=$ODH_MODEL_CONTROLLER_IMAGE"

: "${OPERATOR_TYPE:=odh}"
case "${OPERATOR_TYPE}" in
  odh|opendatahub) OPERATOR_DEPLOYMENT="opendatahub-operator-controller-manager" ;;
  rhods|rhoai)     OPERATOR_DEPLOYMENT="rhods-operator" ;;
  *)               echo "Error: Unknown OPERATOR_TYPE '${OPERATOR_TYPE}'"; exit 1 ;;
esac

_selector=$(oc get deployment "${OPERATOR_DEPLOYMENT}" -n "${OPERATOR_NAMESPACE}" \
  -o go-template='{{range $k,$v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null || true)
_selector="${_selector%,}"
POD_NAME=$(oc get po -n "${OPERATOR_NAMESPACE}" -l "${_selector}" \
  -o jsonpath="{.items[0].metadata.name}" 2>/dev/null || true)

if [ -z "$POD_NAME" ]; then
  echo "Error: Could not find operator pod for deployment ${OPERATOR_DEPLOYMENT}"
  exit 1
fi

echo "Found operator pod: $POD_NAME (deployment: ${OPERATOR_DEPLOYMENT})"

# Clean up any existing manifests in the PVC (but not the mount point itself)
echo "Cleaning up existing manifests in PVC..."
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- bash -c "rm -rf /opt/manifests/kserve/* /opt/manifests/odh-model-controller/*" || true

# Copy config directory to PVC using oc cp
echo "Copying config directory to PVC..."
oc cp "${PROJECT_ROOT}/config/." ${OPERATOR_NAMESPACE}/${POD_NAME}:/opt/manifests/kserve

# Updating params.env
echo ""
echo "Updating params.envs..."
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- bash -c "
  sed -i 's|kserve-controller=.*|kserve-controller=${KSERVE_CONTROLLER_IMAGE}|' /opt/manifests/kserve/overlays/odh/params.env
  sed -i 's|llmisvc-controller=.*|llmisvc-controller=${LLMISVC_CONTROLLER_IMAGE}|' /opt/manifests/kserve/overlays/odh/params.env
  sed -i 's|kserve-agent=.*|kserve-agent=${KSERVE_AGENT_IMAGE}|' /opt/manifests/kserve/overlays/odh/params.env
  sed -i 's|kserve-router=.*|kserve-router=${KSERVE_ROUTER_IMAGE}|' /opt/manifests/kserve/overlays/odh/params.env
  sed -i 's|kserve-storage-initializer=.*|kserve-storage-initializer=${STORAGE_INITIALIZER_IMAGE}|' /opt/manifests/kserve/overlays/odh/params.env
"

# Verify the images were updated
echo ""
echo "Verifying updated KServe params.env:"
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- cat /opt/manifests/kserve/overlays/odh/params.env

# Download and copy odh-model-controller manifests
echo ""
echo "Downloading odh-model-controller manifests from GitHub..."
TEMP_DIR=$(mktemp -d)
trap "rm -rf ${TEMP_DIR}" EXIT

# Download the tarball from GitHub
curl -sL "https://github.com/opendatahub-io/odh-model-controller/tarball/${ODH_MODEL_CONTROLLER_REF}" | tar xz -C ${TEMP_DIR}

# Find the extracted directory (GitHub tarballs extract to org-repo-commit format)
ODH_MC_DIR=$(find ${TEMP_DIR} -maxdepth 1 -type d -name "opendatahub-io-odh-model-controller-*" | head -n 1)

if [ -z "$ODH_MC_DIR" ]; then
  echo "Error: Could not find extracted odh-model-controller directory"
  exit 1
fi

echo "Found odh-model-controller at: $ODH_MC_DIR"

# Copy the config directory to the PVC
echo "Copying odh-model-controller config to PVC..."
oc cp "${ODH_MC_DIR}/config/." ${OPERATOR_NAMESPACE}/${POD_NAME}:/opt/manifests/odh-model-controller/

# Update params.env with PR image
echo ""
echo "Updating odh-model-controller params.env with PR image..."
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- bash -c "
  sed -i 's|odh-model-controller=.*|odh-model-controller=${ODH_MODEL_CONTROLLER_IMAGE}|' /opt/manifests/odh-model-controller/base/params.env
"

# Verify the odh-model-controller params.env was updated
echo ""
echo "Verifying updated odh-model-controller params.env:"
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- cat /opt/manifests/odh-model-controller/base/params.env

# Verify the copy
echo ""
echo "Verifying manifest structure..."
echo "Checking kserve directory..."
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- ls -la /opt/manifests/kserve/
echo ""
echo "Checking kserve overlays/odh directory..."
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- ls -la /opt/manifests/kserve/overlays/odh/
echo ""
echo "Checking odh-model-controller directory..."
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- ls -la /opt/manifests/odh-model-controller/
echo ""
echo "Checking odh-model-controller/base directory..."
oc exec -n ${OPERATOR_NAMESPACE} ${POD_NAME} -- ls -la /opt/manifests/odh-model-controller/base/

echo ""
echo "Manifests successfully copied to PVC!"
echo "  - KServe manifests mounted at: /opt/manifests/kserve/ (from PVC subPath: kserve)"
echo "  - odh-model-controller manifests mounted at: /opt/manifests/odh-model-controller/ (from PVC subPath: odh-model-controller)"
