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
# Deploy the Workload Variant Autoscaler (WVA) controller on OpenShift.
# Uses the ODH fork's cluster-scoped OpenShift Kustomize overlay which
# includes RBAC, monitoring (ServiceMonitor + bearer-token auth), and
# the OpenShift component (Thanos Querier config, service-CA mount).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

WVA_KUSTOMIZE_DIR="${SCRIPT_DIR}/wva"
WVA_NAMESPACE="wva-system"

echo "Deploying WVA (ODH) via kustomize..."

kustomize build "${WVA_KUSTOMIZE_DIR}" | oc apply -f -

echo "Waiting for WVA controller to become ready..."
wait_for_pod_ready "${WVA_NAMESPACE}" "control-plane=controller-manager" 300s

echo "WVA deployed successfully (namespace: ${WVA_NAMESPACE})"
