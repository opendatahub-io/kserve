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
# This script installs OpenShift Serverless operator
# Based on: https://docs.redhat.com/en/documentation/red_hat_openshift_serverless/1.34/html-single/installing_openshift_serverless/index

set -eu

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
source "${SCRIPT_DIR}/common.sh"

echo "Installing OpenShift Serverless operator..."

# Create namespace
oc create namespace openshift-serverless --dry-run=client -o yaml | oc apply -f -

# Check if OperatorGroup already exists, if not create one
OG_COUNT=$(oc get operatorgroup -n openshift-serverless --no-headers 2>/dev/null | wc -l)
if [ "$OG_COUNT" -eq 0 ]; then
  echo "Creating OperatorGroup for openshift-serverless..."
  cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-serverless-og
  namespace: openshift-serverless
spec:
  upgradeStrategy: Default
EOF
else
  echo "OperatorGroup already exists in openshift-serverless namespace"
fi

# Install Serverless operator subscription
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: serverless-operator
  namespace: openshift-serverless
spec:
  channel: stable-1.37
  name: serverless-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF

# Wait for install plan and approve it if needed
echo "Waiting for Serverless operator install plan..."
timeout=60
counter=0
install_plan=""
while [ $counter -lt $timeout ]; do
  install_plan=$(oc get installplan -n openshift-serverless -o json | jq -r '.items[] | select(.spec.clusterServiceVersionNames[]? | contains("serverless-operator")) | select(.spec.approved == false) | .metadata.name' 2>/dev/null | head -1)
  if [ -n "$install_plan" ]; then
    echo "Found install plan: $install_plan, approving..."
    oc patch installplan $install_plan -n openshift-serverless --type merge --patch '{"spec":{"approved":true}}'
    break
  fi
  # Check if already approved or succeeded
  csv_exists=$(oc get csv -n openshift-serverless -o json | jq -r '.items[] | select(.metadata.name | startswith("serverless-operator")) | .metadata.name' 2>/dev/null | head -1)
  if [ -n "$csv_exists" ]; then
    echo "Serverless operator already installing or installed"
    break
  fi
  sleep 2
  counter=$((counter + 2))
done

# Wait for Serverless operator pods to be ready
echo "Waiting for Serverless operator to be ready..."
wait_for_pod_ready "openshift-serverless" "name=knative-openshift" 300s
wait_for_pod_ready "openshift-serverless" "name=knative-openshift-ingress" 300s
wait_for_pod_ready "openshift-serverless" "name=knative-operator" 300s

echo "OpenShift Serverless operator installed successfully"
