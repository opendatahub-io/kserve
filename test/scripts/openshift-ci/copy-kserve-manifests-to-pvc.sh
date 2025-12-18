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

: "${ODH_OPERATOR_NAMESPACE:=openshift-operators}"
: "${KSERVE_MANIFESTS_PVC:=kserve-custom-manifests}"

echo "Copying KServe manifests from current branch to ODH operator PVC..."

# Get the ODH operator pod name
POD_NAME=$(oc get po -l name=opendatahub-operator -n ${ODH_OPERATOR_NAMESPACE} -o jsonpath="{.items[0].metadata.name}")

if [ -z "$POD_NAME" ]; then
  echo "Error: Could not find ODH operator pod"
  exit 1
fi

echo "Found ODH operator pod: $POD_NAME"

# Clean up any existing manifests in the PVC (but not the mount point itself)
echo "Cleaning up existing manifests in PVC..."
oc exec -n ${ODH_OPERATOR_NAMESPACE} ${POD_NAME} -- bash -c "rm -rf /opt/manifests/kserve/*" || true

# Copy config directory to PVC using oc cp
echo "Copying config directory to PVC..."
oc cp "${PROJECT_ROOT}/config/." ${ODH_OPERATOR_NAMESPACE}/${POD_NAME}:/opt/manifests/kserve

# Verify the copy
echo ""
echo "Verifying manifest structure..."
oc exec -n ${ODH_OPERATOR_NAMESPACE} ${POD_NAME} -- ls -la /opt/manifests/kserve/
echo ""
echo "Checking overlays/odh directory..."
oc exec -n ${ODH_OPERATOR_NAMESPACE} ${POD_NAME} -- ls -la /opt/manifests/kserve/overlays/odh/

echo ""
echo "KServe manifests successfully copied to PVC!"
echo "Directory structure is now at: /opt/manifests/kserve/"
