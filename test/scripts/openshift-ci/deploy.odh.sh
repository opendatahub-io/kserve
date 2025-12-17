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

# Step 3: Install ODH operator subscription in openshift-operators (cluster-wide)
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

# Step 6: Wait for ODH operator pod to be ready
echo "Waiting for ODH operator to be ready..."
wait_for_pod_ready "${ODH_OPERATOR_NAMESPACE}" "control-plane=controller-manager"

# Step 7: Wait for CRDs to be established
echo "Waiting for ODH CRDs to be established..."
wait_for_crd "dscinitializations.dscinitialization.opendatahub.io" 90s
wait_for_crd "datascienceclusters.datasciencecluster.opendatahub.io" 90s

echo "ODH operator installed successfully"
echo -e "\n  ODH operator ready to deploy KServe."
echo "  NOTE: Apply DSC/DSCI resources, then patch deployments with PR images."
