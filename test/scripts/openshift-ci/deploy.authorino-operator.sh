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
# This script installs Red Hat Authorino operator via OLM
# Based on: https://catalog.redhat.com/software/containers/3scale-tech-preview/authorino-operator-bundle/65ccf78ad7fa53a5f10e08a0

set -eu

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
source "${SCRIPT_DIR}/common.sh"

echo "Installing Red Hat Authorino operator..."

# Install Authorino operator in openshift-operators namespace (cluster-wide)
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: authorino-operator
  namespace: openshift-operators
spec:
  channel: stable
  installPlanApproval: Automatic
  name: authorino-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF

# Wait for install plan and approve it
echo "Waiting for Authorino install plan to be created..."
timeout=60
counter=0
install_plan=""
while [ $counter -lt $timeout ]; do
  install_plan=$(oc get installplan -n openshift-operators -o json | jq -r '.items[] | select(.spec.clusterServiceVersionNames[]? | contains("authorino-operator")) | select(.spec.approved == false) | .metadata.name' 2>/dev/null | head -1)
  if [ -n "$install_plan" ]; then
    echo "Found install plan: $install_plan"
    break
  fi
  sleep 2
  counter=$((counter + 2))
done

if [ -n "$install_plan" ]; then
  echo "Approving install plan $install_plan..."
  oc patch installplan $install_plan -n openshift-operators --type merge --patch '{"spec":{"approved":true}}'
fi

# Wait for Authorino operator CSV to be ready
echo "Waiting for Authorino operator CSV to be installed..."
timeout=300
counter=0
while [ $counter -lt $timeout ]; do
  csv_status=$(oc get csv -n openshift-operators -o json | jq -r '.items[] | select(.metadata.name | startswith("authorino-operator")) | .status.phase' 2>/dev/null || echo "")
  if [ "$csv_status" = "Succeeded" ]; then
    echo "Authorino operator CSV is ready"
    break
  fi
  echo "Waiting for CSV to be ready... (current status: ${csv_status:-NotFound}, $counter/$timeout)"
  sleep 5
  counter=$((counter + 5))
done

if [ $counter -ge $timeout ]; then
  echo "Timeout waiting for Authorino operator CSV to be ready"
  exit 1
fi

# Wait for Authorino operator pod to be ready
echo "Waiting for Authorino operator pod to be ready..."
wait_for_pod_ready "openshift-operators" "control-plane=authorino-operator" 300s

echo "Red Hat Authorino operator installed successfully"
