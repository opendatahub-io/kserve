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

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

echo "⏳ Installing openshift-lws-operator"

oc create ns "${LWS_NAMESPACE}" || true

{
cat <<EOF | oc create -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: ${LWS_NAME}
  namespace: ${LWS_NAMESPACE}
spec:
  targetNamespaces:
  - ${LWS_NAMESPACE}
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: ${LWS_NAME}
  namespace: ${LWS_NAMESPACE}
spec:
  channel: ${LWS_CHANNEL}
  installPlanApproval: Automatic
  name: ${LWS_NAME}
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF
} || true

wait_for_subscription_csv "${LWS_NAME}" "${LWS_NAMESPACE}" 300
wait_for_crd leaderworkersetoperators.operator.openshift.io 90s

{
cat <<EOF | oc create -f -
apiVersion: operator.openshift.io/v1
kind: LeaderWorkerSetOperator
metadata:
  name: cluster
  namespace: ${LWS_NAMESPACE}
spec:
  managementState: Managed
  logLevel: Normal
  operatorLogLevel: Normal
EOF
} || true

echo "⏳ waiting for openshift-lws-operator to be ready.…"
wait_for_pod_ready "${LWS_NAMESPACE}" "name=openshift-lws-operator"

echo "✅ openshift-lws-operator installed"
