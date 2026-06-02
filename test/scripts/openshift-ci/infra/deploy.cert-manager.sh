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

echo "⏳ Installing cert-manager"
oc create namespace ${CERT_MANAGER_NAMESPACE} || true

{
cat<<EOF | oc create -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: ${CERT_MANAGER_NAME}
  namespace: ${CERT_MANAGER_NAMESPACE}
spec:
  upgradeStrategy: Default
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: ${CERT_MANAGER_NAME}
  namespace: ${CERT_MANAGER_NAMESPACE}
spec:
  channel: ${CERT_MANAGER_CHANNEL}
  installPlanApproval: Automatic
  name: ${CERT_MANAGER_NAME}
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF
} || true

wait_for_subscription_csv "${CERT_MANAGER_NAME}" "${CERT_MANAGER_NAMESPACE}" 300
wait_for_crd certificates.cert-manager.io 90s

echo "✅ Cert-manager installed"
