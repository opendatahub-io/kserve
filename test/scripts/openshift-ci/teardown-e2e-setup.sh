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

# Idempotent teardown of the E2E test environment.
# Works for both raw (manual) and operator-based (ODH/RHOAI) deployments.
# Every delete uses --ignore-not-found / || true so missing resources are skipped.
#
# Ordering matters:
#   1. CI namespace first -- controllers are alive and process finalizers.
#   2. DSC/DSCI + operator -- graceful unmanage, then OLM cleanup.
#   3. KServe overlays, SeaweedFS, ODH Model Controller -- bulk resource removal.
#   4. Webhooks -- cluster-scoped, must go before namespace deletion.
#   5. Application namespaces last.
set -o errexit
set -o nounset
set -o pipefail

MY_PATH=$(dirname "$0")
PROJECT_ROOT=$MY_PATH/../../../
: "${OPERATOR_NAMESPACE:=openshift-operators}"

PARAMS_ENV="$PROJECT_ROOT/config/overlays/odh/params.env"
: "${SKLEARN_IMAGE:=kserve/sklearnserver:latest}"
: "${KSERVE_CONTROLLER_IMAGE:=$(grep '^kserve-controller=' "$PARAMS_ENV" | cut -d= -f2-)}"
: "${KSERVE_AGENT_IMAGE:=$(grep '^kserve-agent=' "$PARAMS_ENV" | cut -d= -f2-)}"
: "${KSERVE_ROUTER_IMAGE:=$(grep '^kserve-router=' "$PARAMS_ENV" | cut -d= -f2-)}"
: "${STORAGE_INITIALIZER_IMAGE:=$(grep '^kserve-storage-initializer=' "$PARAMS_ENV" | cut -d= -f2-)}"
: "${ODH_MODEL_CONTROLLER_IMAGE:=quay.io/opendatahub/odh-model-controller:fast}"

ALL_NAMESPACES=(kserve opendatahub redhat-ods-applications)

echo "Delete CI namespace"
"$MY_PATH/teardown-ci-namespace.sh" "" "kserve-ci-e2e-test"

echo "Deleting DSC / DSCI resources"
oc delete datascienceclusters.datasciencecluster.opendatahub.io --all --ignore-not-found || true
oc delete dscinitializations.dscinitialization.opendatahub.io --all --ignore-not-found || true

echo "Deleting ODH / RHOAI operator OLM resources"
for name in opendatahub-operator rhods-operator; do
  oc delete subscription "${name}" -n "${OPERATOR_NAMESPACE}" --ignore-not-found || true
  oc delete csv -n "${OPERATOR_NAMESPACE}" -l "operators.coreos.com/${name}.${OPERATOR_NAMESPACE}" --ignore-not-found || true
  oc delete catalogsource "${name}-custom-catalog" -n openshift-marketplace --ignore-not-found || true
done
oc delete installplan --all -n "${OPERATOR_NAMESPACE}" --ignore-not-found || true

echo "Deleting custom-manifests PVC"
oc delete pvc kserve-custom-manifests -n "${OPERATOR_NAMESPACE}" --ignore-not-found || true

echo "Deleting ImageDigestMirrorSets created for operator install"
idms=rhoai-quay-mirror
oc delete imagedigestmirrorset "${idms}" --ignore-not-found || true

echo "Deleting KServe (raw overlay, if present)"
kustomize build "$PROJECT_ROOT/config/overlays/odh-test" 2>/dev/null |
  oc delete --ignore-not-found -f - || true
kustomize build "$PROJECT_ROOT/config/overlays/test" 2>/dev/null |
  oc delete --ignore-not-found -f - || true

echo "Deleting TLS SeaweedFS resources and generated certificates"
kustomize build "$PROJECT_ROOT/test/overlays/openshift-ci" 2>/dev/null |
  oc delete --ignore-not-found -f - || true
for ns in "${ALL_NAMESPACES[@]}"; do
  oc delete secret seaweedfs-tls-custom -n "$ns" --ignore-not-found || true
  oc delete secret seaweedfs-tls-serving -n "$ns" --ignore-not-found || true
done
rm -rf "$PROJECT_ROOT/test/scripts/openshift-ci/tls/certs"

for ns in "${ALL_NAMESPACES[@]}"; do
  kustomize build "$PROJECT_ROOT/config/overlays/test/s3-local-backend" 2>/dev/null |
    sed "s/namespace: kserve/namespace: ${ns}/" |
    oc delete -n "$ns" --ignore-not-found -f - || true
done

echo "Deleting ODH Model Controller"
kustomize build "$PROJECT_ROOT/test/scripts/openshift-ci" 2>/dev/null |
  oc delete --ignore-not-found -f - || true
for ns in "${ALL_NAMESPACES[@]}"; do
  oc wait --for=delete pod -l app=odh-model-controller -n "$ns" --timeout=30s 2>/dev/null || true
done

echo "Deleting NetworkPolicy"
for ns in "${ALL_NAMESPACES[@]}"; do
  oc delete networkpolicy allow-all -n "$ns" --ignore-not-found || true
done

echo "Delete CMA / KEDA operator"
oc delete kedacontroller -n openshift-keda keda --ignore-not-found || true
oc delete subscription -n openshift-keda openshift-custom-metrics-autoscaler-operator --ignore-not-found || true
oc delete namespace openshift-keda --ignore-not-found || true

# Webhooks must go before namespaces -- failurePolicy=Fail with DELETE rules
# blocks namespace deletion when the webhook service is already gone.
echo "Deleting KServe webhook configurations"
for name in $(oc get validatingwebhookconfiguration -o name 2>/dev/null | grep kserve); do
  oc delete "$name" --ignore-not-found || true
done
for name in $(oc get mutatingwebhookconfiguration -o name 2>/dev/null | grep kserve); do
  oc delete "$name" --ignore-not-found || true
done

# CRDs carry status.storedVersions across installs; version downgrades
# (e.g. 3.4 -> 3.3) are rejected by the API server when old versions
# remain in storedVersions.  Deleting them lets the next operator
# recreate them cleanly.
echo "Deleting KServe CRDs"
for crd in $(oc get crd -o name 2>/dev/null | grep '\.serving\.kserve\.io$'); do
  oc delete "$crd" --ignore-not-found || true
done

# WARNING: this deletes ALL *.opendatahub.io CRDs cluster-wide (not just
# KServe-related ones). This is intentional -- it lets version downgrades
# succeed by clearing storedVersions that would otherwise be rejected.
# This script must only be run against a DEDICATED test cluster.
echo "Deleting ODH / RHOAI platform CRDs"
for crd in $(oc get crd -o name 2>/dev/null | grep '\.opendatahub\.io$'); do
  oc delete "$crd" --ignore-not-found || true
done

echo "Deleting application namespaces"
for ns in "${ALL_NAMESPACES[@]}"; do
    oc delete namespace "$ns" --ignore-not-found --timeout=60s || true
    if oc get namespace "$ns" &>/dev/null; then
        echo "  Namespace ${ns} still terminating -- stripping finalizers from stuck resources..."
        for resource in inferenceservices.serving.kserve.io \
                        inferencegraphs.serving.kserve.io \
                        datascienceclusters.datasciencecluster.opendatahub.io \
                        dscinitializations.dscinitialization.opendatahub.io; do
            for obj in $(oc get "$resource" -n "$ns" -o name 2>/dev/null); do
                oc patch "$obj" -n "$ns" --type=merge \
                    -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
            done
        done
        oc wait --for=delete "namespace/${ns}" --timeout=60s 2>/dev/null || \
            echo "WARNING: namespace ${ns} did not terminate within timeout"
    fi
done

echo "Teardown complete"
