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
# Create the ClusterTriggerAuthentication that allows KEDA ScaledObjects
# to query Thanos Querier using bearer-token authentication.
#
# The LLMISVC controller references this auth by name
# ("ai-inference-keda-thanos") via the autoscaling-wva-controller-config
# in inferenceservice-config.

set -euo pipefail

: "${KEDA_NAMESPACE:=openshift-keda}"
KEDA_SA="keda-operator"

echo "Creating ServiceAccount token secret for KEDA..."
cat <<EOF | oc apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: ${KEDA_SA}-token
  namespace: ${KEDA_NAMESPACE}
  annotations:
    kubernetes.io/service-account.name: ${KEDA_SA}
type: kubernetes.io/service-account-token
EOF

echo "Waiting for token to be populated..."
for i in $(seq 1 30); do
  TOKEN=$(oc get secret "${KEDA_SA}-token" -n "${KEDA_NAMESPACE}" \
    -o jsonpath='{.data.token}' 2>/dev/null || true)
  if [[ -n "$TOKEN" ]]; then
    break
  fi
  sleep 2
done
if [[ -z "${TOKEN:-}" ]]; then
  echo "ERROR: Timed out waiting for SA token secret to be populated" >&2
  exit 1
fi

echo "Granting cluster-monitoring-view to KEDA SA..."
cat <<EOF | oc apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: keda-thanos-sa-monitoring-view
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-monitoring-view
subjects:
- kind: ServiceAccount
  name: ${KEDA_SA}
  namespace: ${KEDA_NAMESPACE}
EOF

echo "Extracting Thanos Querier CA certificate..."
THANOS_CA=""
if oc get configmap serving-certs-ca-bundle -n openshift-monitoring -o jsonpath='{.data.service-ca\.crt}' &>/dev/null; then
  THANOS_CA=$(oc get configmap serving-certs-ca-bundle -n openshift-monitoring \
    -o jsonpath='{.data.service-ca\.crt}')
elif oc get configmap openshift-service-ca.crt -n openshift-config-managed -o jsonpath='{.data.service-ca\.crt}' &>/dev/null; then
  THANOS_CA=$(oc get configmap openshift-service-ca.crt -n openshift-config-managed \
    -o jsonpath='{.data.service-ca\.crt}')
fi

echo "Creating ClusterTriggerAuthentication ai-inference-keda-thanos..."
if [[ -n "$THANOS_CA" ]]; then
  cat <<EOF | oc apply -f -
apiVersion: keda.sh/v1alpha1
kind: ClusterTriggerAuthentication
metadata:
  name: ai-inference-keda-thanos
spec:
  secretTargetRef:
    - parameter: bearerToken
      name: ${KEDA_SA}-token
      namespace: ${KEDA_NAMESPACE}
      key: token
    - parameter: ca
      name: ${KEDA_SA}-token
      namespace: ${KEDA_NAMESPACE}
      key: service-ca.crt
EOF

  echo "Injecting Thanos CA into the SA token secret..."
  CA_B64=$(echo -n "$THANOS_CA" | base64 -w 0)
  oc patch secret "${KEDA_SA}-token" -n "${KEDA_NAMESPACE}" --type=merge \
    -p "{\"data\":{\"service-ca.crt\":\"${CA_B64}\"}}"
else
  echo "WARNING: Could not extract Thanos CA; creating auth without CA parameter."
  echo "KEDA will use system trust store for TLS verification."
  cat <<EOF | oc apply -f -
apiVersion: keda.sh/v1alpha1
kind: ClusterTriggerAuthentication
metadata:
  name: ai-inference-keda-thanos
spec:
  secretTargetRef:
  - parameter: bearerToken
    name: ${KEDA_SA}-token
    namespace: ${KEDA_NAMESPACE}
    key: token
EOF
fi

echo "ClusterTriggerAuthentication ai-inference-keda-thanos created"
