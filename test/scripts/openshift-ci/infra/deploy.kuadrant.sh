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

# Installs the upstream Kuadrant operator via Helm chart and configures
# Authorino with TLS for OpenShift.  Replaces the previous RHCL (OLM-based)
# installation to allow tracking upstream releases independently of the
# Red Hat Connectivity Link productization cadence.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
source "$SCRIPT_DIR/../common.sh"

"${PROJECT_ROOT}/hack/setup/cli/install-helm.sh"
export PATH="${PROJECT_ROOT}/bin:${PATH}"

# How many times to wait for Ready before delete/recreate and final failure.
KUADRANT_READY_MAX_ATTEMPTS="${KUADRANT_READY_MAX_ATTEMPTS:-2}"
# Seconds to sleep after deleting Kuadrant before recreating (stabilization).
KUADRANT_POST_DELETE_SLEEP="${KUADRANT_POST_DELETE_SLEEP:-15}"
# Per-attempt timeout for oc wait on Kuadrant Ready.
KUADRANT_READY_TIMEOUT="${KUADRANT_READY_TIMEOUT:-5m}"

create_kuadrant_cr() {
  oc create -f - <<EOF
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: ${KUADRANT_NS}
EOF
}

echo "⏳ Installing Kuadrant operator v${KUADRANT_VERSION} via Helm"
oc create ns "${KUADRANT_NS}" || true

helm repo add kuadrant "${KUADRANT_HELM_REPO}" --force-update
helm repo update kuadrant

helm install kuadrant-operator kuadrant/kuadrant-operator \
  --namespace "${KUADRANT_NS}" \
  --version "${KUADRANT_VERSION}" \
  --wait \
  --timeout 10m

wait_for_crd kuadrants.kuadrant.io 90s
wait_for_api_discovery "kuadrant.io/v1beta1" "kuadrants" 120

create_kuadrant_cr || true

kuadrant_ready_attempt=1
while (( kuadrant_ready_attempt <= KUADRANT_READY_MAX_ATTEMPTS )); do
  echo "⏳ waiting for Kuadrant Ready (attempt ${kuadrant_ready_attempt}/${KUADRANT_READY_MAX_ATTEMPTS}, timeout ${KUADRANT_READY_TIMEOUT})…"
  if oc wait Kuadrant -n "${KUADRANT_NS}" kuadrant --for=condition=Ready --timeout="${KUADRANT_READY_TIMEOUT}"; then
    break
  fi
  if (( kuadrant_ready_attempt >= KUADRANT_READY_MAX_ATTEMPTS )); then
    oc get Kuadrant -n "${KUADRANT_NS}" kuadrant -oyaml
    oc get pods -n "${KUADRANT_NS}" -oyaml
    oc get deployments -n "${KUADRANT_NS}" -oyaml

    oc describe Kuadrant -n "${KUADRANT_NS}" kuadrant
    oc describe pods -n "${KUADRANT_NS}"
    oc describe deployments -n "${KUADRANT_NS}"

    echo "=== Controller manager logs ==="
    oc logs -n "${KUADRANT_NS}" deployment/kuadrant-operator-controller-manager --tail=200 || true
    exit 1
  fi
  echo "Kuadrant not Ready; deleting and recreating CR…"
  oc delete kuadrant kuadrant -n "${KUADRANT_NS}" --ignore-not-found=true --wait=true --timeout=300s
  echo "⏳ sleeping ${KUADRANT_POST_DELETE_SLEEP}s before recreating Kuadrant…"
  sleep "${KUADRANT_POST_DELETE_SLEEP}"
  create_kuadrant_cr || true
  kuadrant_ready_attempt=$((kuadrant_ready_attempt + 1))
done

wait_for_pod_ready "${KUADRANT_NS}" "control-plane=authorino-operator"

# Wait for Authorino service to be created
echo "⏳ waiting for authorino service to be created..."
cert_secret="authorino-server-cert"
oc wait --for=jsonpath='{.metadata.name}'=authorino-authorino-authorization svc/authorino-authorino-authorization -n "${KUADRANT_NS}" --timeout=2m

oc annotate svc/authorino-authorino-authorization service.beta.openshift.io/serving-cert-secret-name="${cert_secret}" -n "${KUADRANT_NS}"

# Update Authorino to configure SSL
oc apply -f - <<EOF
apiVersion: operator.authorino.kuadrant.io/v1beta1
kind: Authorino
metadata:
  name: authorino
  namespace: ${KUADRANT_NS}
spec:
  replicas: 1
  clusterWide: true
  logLevel: debug
  listener:
    tls:
      enabled: true
      certSecretRef:
        name: authorino-server-cert
  oidcServer:
    tls:
      enabled: false
EOF

wait_for_pod_ready "${KUADRANT_NS}" "control-plane=authorino-operator"

echo "✅ Kuadrant v${KUADRANT_VERSION} (authorino) installed"
