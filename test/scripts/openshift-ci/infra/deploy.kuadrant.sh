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
KUADRANT_NS="${KUADRANT_NS:-kuadrant-system}"

echo "⏳ Installing RHCL(Kuadrant) operator"
oc create ns ${KUADRANT_NS} || true

{
cat <<EOF | oc create -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: rhcl-operator
  namespace: ${KUADRANT_NS}
spec:
  config:
    env:
      - name: ISTIO_GATEWAY_CONTROLLER_NAMES
        value: openshift.io/gateway-controller/v1
  channel: stable
  installPlanApproval: Automatic
  name: rhcl-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
---
kind: OperatorGroup
apiVersion: operators.coreos.com/v1
metadata:
  name: kuadrant
  namespace: ${KUADRANT_NS}
spec:
  upgradeStrategy: Default
EOF
} || true

wait_for_crd  kuadrants.kuadrant.io  90s

{
cat <<EOF | oc create -f -
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: ${KUADRANT_NS}
EOF
} || true

echo "⏳ waiting for authorino-operator to be ready.…"
wait_for_pod_ready "${KUADRANT_NS}" "authorino-resource=authorino"

echo "✅ kuadrant(authorino) installed"
