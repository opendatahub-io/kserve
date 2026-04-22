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
# This script installs the ODH operator and configures it to use custom KServe manifests
# Based on: https://github.com/opendatahub-io/opendatahub-operator/blob/main/hack/component-dev/README.md
#
# NOTE: This is for development/testing only, not for production use

set -eu

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
PROJECT_ROOT="${SCRIPT_DIR}/../../../"
source "${SCRIPT_DIR}/common.sh"
source "${SCRIPT_DIR}/install-operator.sh"

: "${ODH_OPERATOR_NAMESPACE:=openshift-operators}"

# Map legacy env vars to the shared install-operator.sh interface.
# OPERATOR_VERSION is intentionally left unset (CI mode: skip-if-installed,
# Automatic approval, no startingCSV).
: "${OPERATOR_TYPE:=odh}"
: "${CHANNEL_OVERRIDE:=${ODH_OPERATOR_CHANNEL:-}}"
: "${CATALOG_SOURCE:=${ODH_OPERATOR_SOURCE:-}}"

echo "Installing ODH operator stack to manage KServe deployment..."

install_operator

# Wait for ODH CRDs to be established
echo "Waiting for ODH CRDs to be established..."
wait_for_crd "dscinitializations.dscinitialization.opendatahub.io" 90s
wait_for_crd "datascienceclusters.datasciencecluster.opendatahub.io" 90s

: "${COPY_PR_MANIFESTS:=true}"

if [[ "$COPY_PR_MANIFESTS" == "true" ]]; then
  echo "Configuring operator to use custom KServe manifests from PR..."

  echo "Creating PVC for custom KServe manifests..."
  oc apply -f "${SCRIPT_DIR}/odh-operator-custom-manifests/pvc.yaml"
  echo "PVC created (will bind when consumed by operator pod)"

  echo "Patching operator CSV to mount custom manifests volume..."
  CSV=$(oc get csv -n ${ODH_OPERATOR_NAMESPACE} -o name | grep "${OPERATOR_NAME}" | head -n1 | cut -d/ -f2)
  echo "Found CSV: $CSV"

  if oc get csv "$CSV" -n ${ODH_OPERATOR_NAMESPACE} -o json | jq -e '.spec.install.spec.deployments[0].spec.template.spec.volumes[] | select(.name=="kserve-custom-manifests")' > /dev/null 2>&1; then
    echo "Volume already mounted, skipping patch"
  else
    echo "Applying CSV patch to mount custom manifests volume..."
    oc patch csv "$CSV" -n ${ODH_OPERATOR_NAMESPACE} --type json --patch-file "${SCRIPT_DIR}/odh-operator-custom-manifests/csv-patch.json"
  fi

  OPERATOR_POD_SELECTOR=$(oc get deployment "${CONTROLLER_DEPLOYMENT}" -n "${ODH_OPERATOR_NAMESPACE}" \
    -o json 2>/dev/null | python3 -c "
import json, sys
d = json.load(sys.stdin)['spec']['selector']['matchLabels']
print(','.join(f'{k}={v}' for k, v in d.items()))
" 2>/dev/null || echo "name=${OPERATOR_NAME}")

  echo "Waiting for operator pod to restart with custom manifests volume (selector: ${OPERATOR_POD_SELECTOR})..."
  oc wait --for='jsonpath={.status.conditions[?(@.type=="Ready")].status}=True' \
    pod -l "${OPERATOR_POD_SELECTOR}" -n ${ODH_OPERATOR_NAMESPACE} \
    --timeout=300s 2>/dev/null || true

  sleep 5

  wait_for_pod_ready "${ODH_OPERATOR_NAMESPACE}" "${OPERATOR_POD_SELECTOR}" 300s

  echo "Operator ready to use custom KServe manifests."
  echo "  NOTE: Copy PR manifests to PVC, then apply DSC/DSCI resources."
else
  echo "Vanilla operator install -- skipping PVC/CSV patch (using bundled manifests)"
fi

echo "ODH operator installed successfully"
