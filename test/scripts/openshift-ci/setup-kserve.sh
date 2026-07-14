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
# Dispatches to the correct KServe install method and runs common post-install steps.
# Called by setup-e2e-tests.sh, or directly via "make deploy-ocp".
#
# Env-var interface:
#   OPERATOR_TYPE     odh | opendatahub | rhoai | rhods | empty (manual kustomize)
#   KSERVE_NAMESPACE  override; derived from OPERATOR_TYPE when not set
#   ... (plus all vars consumed by the selected deploy script)
#
# Positional args:
#   $1   E2E marker (e.g. llminferenceservice, raw, llm-d) -- gates optional infra;
#        omit or pass empty string to skip those code paths
#
# To add a new install method:
#   1. Create deploy.<method>.sh (set SEAWEEDFS_BUNDLED appropriately in its header)
#   2. Add a case branch below

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
source "${SCRIPT_DIR}/common.sh"
source "${SCRIPT_DIR}/version.sh"

: "${OPERATOR_TYPE:=}"

# Derive KSERVE_NAMESPACE from OPERATOR_TYPE. When called from setup-e2e-tests.sh,
# KSERVE_NAMESPACE is already exported and these :=  defaults are no-ops.
case "${OPERATOR_TYPE}" in
  rhods|rhoai)     : "${KSERVE_NAMESPACE:=redhat-ods-applications}" ;;
  odh|opendatahub) : "${KSERVE_NAMESPACE:=opendatahub}" ;;
  "")              : "${KSERVE_NAMESPACE:=kserve}" ;;
  *)               echo "Unknown OPERATOR_TYPE '${OPERATOR_TYPE}'"; exit 1 ;;
esac
export KSERVE_NAMESPACE

# Install kustomize/yq needed by deploy.kserve-manual.sh and the ODH-MC kustomize build.
# Also done by setup-e2e-tests.sh for SeaweedFS; the install script is idempotent.
"${PROJECT_ROOT}/hack/setup/cli/install-kustomize.sh"
make -C "${PROJECT_ROOT}" yq
export PATH="${PROJECT_ROOT}/bin:${PATH}"

# Ensure the target namespace exists before the install scripts apply resources.
oc new-project "${KSERVE_NAMESPACE}" || true

# --- Dispatch to the selected install method ---
case "${OPERATOR_TYPE}" in
  odh|opendatahub|rhoai|rhods) "${SCRIPT_DIR}/deploy.odh.sh" ;;
  "")                          "${SCRIPT_DIR}/deploy.kserve-manual.sh" "${1:-}" ;;
esac

# --- Common post-install steps (apply to all install methods) ---

# On OCP 4.20 and earlier, InferencePool lives in the x-k8s.io API group.
# OCP 4.21+ ships the GA API group (inference.networking.k8s.io).
if [[ -z "${INFERENCE_POOL_GROUP:-}" ]]; then
  server_version=$(get_openshift_server_version)
  ocp_major_minor=$(echo "$server_version" | awk -F. '{print $1"."$2}')
  if awk "BEGIN{exit !($ocp_major_minor <= 4.20)}"; then
    export INFERENCE_POOL_GROUP="inference.networking.x-k8s.io"
    echo "OCP $server_version (${ocp_major_minor}): using INFERENCE_POOL_GROUP=$INFERENCE_POOL_GROUP"
  else
    echo "OCP $server_version (${ocp_major_minor}): skipping setting INFERENCE_POOL_GROUP env variable."
  fi
fi

# LLMISvc dependencies: cert-manager, LeaderWorkerSet, Gateway API, Kuadrant.
# These are core cluster infrastructure required for LLMInferenceService to function.
if [[ "${1:-}" =~ "llminferenceservice" ]]; then
  "${SCRIPT_DIR}/setup-llm.sh" --skip-kserve --deploy-kuadrant
fi

# Early CA cert extraction for raw deployments. This reads from cluster-wide namespaces
# (not KSERVE_NAMESPACE) so it can run immediately after the install.
# setup-e2e-tests.sh overwrites /tmp/ca.crt later with the namespace-scoped cert for
# the Python test client; both writes are intentional.
if [[ "${1:-}" =~ raw ]]; then
  echo "Extracting OpenShift CA certificates for raw deployment..."
  {
    oc get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}' 2>/dev/null && echo ""
    oc get configmap openshift-service-ca.crt -n openshift-config-managed \
      -o jsonpath='{.data.service-ca\.crt}' 2>/dev/null || \
    oc get secret service-ca -n openshift-service-ca \
      -o jsonpath='{.data.service-ca\.crt}' 2>/dev/null | base64 -d || true
  } > /tmp/ca.crt
  if [ -s "/tmp/ca.crt" ] && grep -q "BEGIN CERTIFICATE" "/tmp/ca.crt"; then
    echo "CA certificate bundle extracted ($(grep -c "BEGIN CERTIFICATE" /tmp/ca.crt) certificates)"
  else
    echo "Warning: failed to extract CA certificates"
  fi
fi

# Patch inferenceservice-config with the cluster ingress domain and restart the controller.
echo "Patching ingress domain..."
export OPENSHIFT_INGRESS_DOMAIN
OPENSHIFT_INGRESS_DOMAIN=$(oc get ingresses.config cluster -o jsonpath='{.spec.domain}')
INGRESS_DATA=$(oc get configmap inferenceservice-config -n "${KSERVE_NAMESPACE}" \
  -o jsonpath='{.data.ingress}' | \
  yq -p json -o json '.ingressDomain = strenv(OPENSHIFT_INGRESS_DOMAIN)')
oc patch configmap inferenceservice-config -n "${KSERVE_NAMESPACE}" --type=merge \
  -p "$(INGRESS="$INGRESS_DATA" yq -n -o json '.data.ingress = strenv(INGRESS)')"
oc delete pod -n "${KSERVE_NAMESPACE}" -l control-plane=kserve-controller-manager

echo "Waiting for kserve-controller-manager to be ready..."
oc wait --for=condition=ready pod -l control-plane=kserve-controller-manager \
  -n "${KSERVE_NAMESPACE}" --timeout=300s

# Install or wait for ODH Model Controller depending on install method.
if [[ -z "${OPERATOR_TYPE}" ]]; then
  # Manual: install odh-model-controller directly via kustomize.
  # TODO: can be moved to odh-test overlays
  echo "Installing ODH Model Controller manually..."
  ODH_MC_KUSTOMIZE_DIR="${SCRIPT_DIR}"
  if [[ -n "${ODH_MC_MANIFEST_SOURCE:-}" ]]; then
    echo "Overriding odh-model-controller manifests source to: ${ODH_MC_MANIFEST_SOURCE}"
    # Copy to bin/ so the original kustomization.yaml is never modified.
    ODH_MC_KUSTOMIZE_DIR="${PROJECT_ROOT}/bin/odh-mc-kustomize"
    mkdir -p "${ODH_MC_KUSTOMIZE_DIR}"
    # kustomize requires relative paths in resources
    ODH_MC_MANIFEST_REL="$(realpath --relative-to="${ODH_MC_KUSTOMIZE_DIR}" "${ODH_MC_MANIFEST_SOURCE}")"
    cat > "${ODH_MC_KUSTOMIZE_DIR}/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - ${ODH_MC_MANIFEST_REL}

namespace: ${KSERVE_NAMESPACE}
EOF
  fi
  kustomize build --load-restrictor LoadRestrictionsNone "${ODH_MC_KUSTOMIZE_DIR}" | \
    oc apply -n "${KSERVE_NAMESPACE}" -f -
  oc rollout status deployment/odh-model-controller -n "${KSERVE_NAMESPACE}" --timeout=300s
else
  # Operator-deployed: wait for odh-model-controller to become ready.
  # The image was already configured via deploy.odh.sh (copy-kserve-manifests-to-pvc.sh).
  echo "Waiting for ODH operator to deploy ODH Model Controller..."
  wait_for_pod_ready "${KSERVE_NAMESPACE}" "app=odh-model-controller" 600s
  echo "Verifying ODH Model Controller deployment..."
  oc rollout status deployment/odh-model-controller -n "${KSERVE_NAMESPACE}" --timeout=300s
  ACTUAL_IMAGE=$(oc get deployment odh-model-controller -n "${KSERVE_NAMESPACE}" \
    -o jsonpath='{.spec.template.spec.containers[0].image}')
  echo "ODH Model Controller deployed with image: $ACTUAL_IMAGE"
fi

# Allow all ingress/egress in KSERVE_NAMESPACE.
# Required for webhook communication; without this the webhook returns HTTP 500.
cat <<EOF | oc apply -f -
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-all
  namespace: ${KSERVE_NAMESPACE}
spec:
  podSelector: {}
  ingress:
  - {}
  egress:
  - {}
  policyTypes:
  - Ingress
  - Egress
EOF

echo "KServe setup complete (namespace: ${KSERVE_NAMESPACE})"
