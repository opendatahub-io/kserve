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

# Set default values for ODH operator configuration
: "${ODH_OPERATOR_NAMESPACE:=openshift-operators}"
: "${ODH_OPERATOR_CHANNEL:=fast-3}"
: "${ODH_OPERATOR_SOURCE:=community-operators}"
: "${ODH_OPERATOR_SOURCE_NAMESPACE:=openshift-marketplace}"

echo "Installing ODH operator stack to manage KServe deployment..."
echo "  Namespace: ${ODH_OPERATOR_NAMESPACE}"
echo "  Channel: ${ODH_OPERATOR_CHANNEL}"
echo "  Source: ${ODH_OPERATOR_SOURCE}"

# Step 1: Install Authorino
echo "Installing Red Hat Authorino Operator..."
${SCRIPT_DIR}/deploy.authorino-operator.sh

# Step 2: Install Serverless
echo "Installing OpenShift Serverless..."
${SCRIPT_DIR}/deploy.serverless-operator.sh

# Step 3: Check if ODH operator is already installed
csv_status=$(oc get csv -n ${ODH_OPERATOR_NAMESPACE} -o json 2>/dev/null | jq -r '.items[] | select(.metadata.name | startswith("opendatahub-operator")) | .status.phase' 2>/dev/null || echo "")

if [ "$csv_status" = "Succeeded" ]; then
  echo "ODH operator already installed and ready, skipping installation"
else
  # Install ODH operator subscription in openshift-operators (cluster-wide)
  echo "Installing ODH operator..."
  # Note: No need to create namespace or OperatorGroup - openshift-operators already has them
  cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  labels:
    operators.coreos.com/opendatahub-operator.${ODH_OPERATOR_NAMESPACE}: ""
  name: opendatahub-operator
  namespace: ${ODH_OPERATOR_NAMESPACE}
spec:
  channel: ${ODH_OPERATOR_CHANNEL}
  installPlanApproval: Automatic
  name: opendatahub-operator
  source: ${ODH_OPERATOR_SOURCE}
  sourceNamespace: ${ODH_OPERATOR_SOURCE_NAMESPACE}
EOF

  # Step 4: Wait for install plan and approve it
  echo "Waiting for ODH operator install plan to be created..."
  timeout=60
  counter=0
  install_plan=""
  while [ $counter -lt $timeout ]; do
    install_plan=$(oc get installplan -n ${ODH_OPERATOR_NAMESPACE} -o json | jq -r '.items[] | select(.spec.clusterServiceVersionNames[]? | contains("opendatahub-operator")) | select(.spec.approved == false) | .metadata.name' 2>/dev/null | head -1)
    if [ -n "$install_plan" ]; then
      echo "Found install plan: $install_plan"
      break
    fi
    sleep 2
    counter=$((counter + 2))
  done

  if [ -n "$install_plan" ]; then
    echo "Approving install plan $install_plan..."
    oc patch installplan $install_plan -n ${ODH_OPERATOR_NAMESPACE} --type merge --patch '{"spec":{"approved":true}}'
  fi

  # Step 5: Wait for ODH operator CSV to be ready
  echo "Waiting for ODH operator CSV to be installed..."
  timeout=300
  counter=0
  while [ $counter -lt $timeout ]; do
    csv_status=$(oc get csv -n ${ODH_OPERATOR_NAMESPACE} -o json | jq -r '.items[] | select(.metadata.name | startswith("opendatahub-operator")) | .status.phase' 2>/dev/null || echo "")
    if [ "$csv_status" = "Succeeded" ]; then
      echo "ODH operator CSV is ready"
      break
    fi
    echo "Waiting for CSV to be ready... (current status: ${csv_status:-NotFound}, $counter/$timeout)"
    sleep 5
    counter=$((counter + 5))
  done

  if [ $counter -ge $timeout ]; then
    echo "Timeout waiting for ODH operator CSV to be ready"
    exit 1
  fi
fi

# Step 6: Wait for ODH operator pod to be ready
echo "Waiting for ODH operator to be ready..."
wait_for_pod_ready "${ODH_OPERATOR_NAMESPACE}" "control-plane=controller-manager"

# Step 7: Wait for CRDs to be established
echo "Waiting for ODH CRDs to be established..."
wait_for_crd "dscinitializations.dscinitialization.opendatahub.io" 90s
wait_for_crd "datascienceclusters.datasciencecluster.opendatahub.io" 90s

# Step 8: Configure operator to use custom KServe manifests from PR
echo "Configuring ODH operator to use custom KServe manifests from PR..."

# Create PVC for custom manifests
echo "Creating PVC for custom KServe manifests..."
oc apply -f "${SCRIPT_DIR}/odh-operator-custom-manifests/pvc.yaml"

# Note: PVC may stay in Pending state with WaitForFirstConsumer binding mode
# It will only bind when a pod (the operator) actually consumes it
echo "PVC created (will bind when consumed by operator pod)"

# Patch CSV to mount custom manifests volume
echo "Patching ODH operator CSV to mount custom manifests volume..."
CSV=$(oc get csv -n ${ODH_OPERATOR_NAMESPACE} -o name | grep opendatahub-operator | head -n1 | cut -d/ -f2)
echo "Found CSV: $CSV"

# Check if volume is already mounted
if oc get csv "$CSV" -n ${ODH_OPERATOR_NAMESPACE} -o json | jq -e '.spec.install.spec.deployments[0].spec.template.spec.volumes[] | select(.name=="kserve-custom-manifests")' > /dev/null 2>&1; then
  echo "Volume already mounted, skipping patch"
else
  echo "Applying CSV patch to mount custom manifests volume..."
  oc patch csv "$CSV" -n ${ODH_OPERATOR_NAMESPACE} --type json --patch-file "${SCRIPT_DIR}/odh-operator-custom-manifests/csv-patch.json"
fi

# Wait for operator pod to restart with volume mounted
echo "Waiting for ODH operator pod to restart with custom manifests volume..."
oc wait --for='jsonpath={.status.conditions[?(@.type=="Ready")].status}=True' \
  pod -l name=opendatahub-operator -n ${ODH_OPERATOR_NAMESPACE} \
  --timeout=300s 2>/dev/null || true

# Give it a moment to stabilize
sleep 5

# Verify pod is ready with volume mounted
wait_for_pod_ready "${ODH_OPERATOR_NAMESPACE}" "name=opendatahub-operator" 300s

echo "ODH operator installed successfully"
echo -e "\n  ODH operator ready to use custom KServe manifests."
echo "  NOTE: Copy PR manifests to PVC, then apply DSC/DSCI resources."
