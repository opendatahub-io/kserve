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
# Every delete uses --ignore-not-found / || true so resources that were never
# created are silently skipped.
set -o errexit
set -o nounset
set -o pipefail

MY_PATH=$(dirname "$0")
PROJECT_ROOT=$MY_PATH/../../../

PARAMS_ENV="$PROJECT_ROOT/config/overlays/odh/params.env"
: "${SKLEARN_IMAGE:=kserve/sklearnserver:latest}"
: "${KSERVE_CONTROLLER_IMAGE:=$(grep '^kserve-controller=' "$PARAMS_ENV" | cut -d= -f2-)}"
: "${KSERVE_AGENT_IMAGE:=$(grep '^kserve-agent=' "$PARAMS_ENV" | cut -d= -f2-)}"
: "${KSERVE_ROUTER_IMAGE:=$(grep '^kserve-router=' "$PARAMS_ENV" | cut -d= -f2-)}"
: "${STORAGE_INITIALIZER_IMAGE:=$(grep '^kserve-storage-initializer=' "$PARAMS_ENV" | cut -d= -f2-)}"
: "${ODH_MODEL_CONTROLLER_IMAGE:=quay.io/opendatahub/odh-model-controller:fast}"

ALL_NAMESPACES=(kserve opendatahub redhat-ods-applications)

# ---------------------------------------------------------------------------
# 1. DSC / DSCI -- delete first so the operator can gracefully unmanage
# ---------------------------------------------------------------------------
echo "Deleting DSC / DSCI resources"
oc delete datascienceclusters.datasciencecluster.opendatahub.io --all --ignore-not-found || true
oc delete dscinitializations.dscinitialization.opendatahub.io --all --ignore-not-found || true

# ---------------------------------------------------------------------------
# 2. ODH / RHOAI operator (Subscription, CSV, CatalogSource, PVC, IDMS)
# ---------------------------------------------------------------------------
echo "Deleting ODH / RHOAI operator OLM resources"
for name in opendatahub-operator rhods-operator; do
  oc delete subscription "${name}" -n openshift-operators --ignore-not-found || true
  oc delete csv -n openshift-operators -l "operators.coreos.com/${name}.openshift-operators" --ignore-not-found || true
  oc delete catalogsource "${name}-custom-catalog" -n openshift-marketplace --ignore-not-found || true
done

echo "Deleting custom-manifests PVC"
oc delete pvc kserve-custom-manifests -n openshift-operators --ignore-not-found || true

echo "Deleting ImageDigestMirrorSets created for operator install"
for idms in rhoai-quay-mirror; do
  oc delete imagedigestmirrorset "${idms}" --ignore-not-found || true
done

# ---------------------------------------------------------------------------
# 3. KServe components (covers both raw-kustomize and operator-managed)
# ---------------------------------------------------------------------------
echo "Deleting KServe (raw overlay, if present)"
kustomize build "$PROJECT_ROOT/config/overlays/odh-test" 2>/dev/null |
  oc delete --ignore-not-found -f - || true

# Also try the legacy test overlay in case it was used
kustomize build "$PROJECT_ROOT/config/overlays/test" 2>/dev/null |
  oc delete --ignore-not-found -f - || true

# ---------------------------------------------------------------------------
# 4. SeaweedFS S3 TLS resources
# ---------------------------------------------------------------------------
echo "Deleting TLS SeaweedFS resources and generated certificates"
kustomize build "$PROJECT_ROOT/test/overlays/openshift-ci" 2>/dev/null |
  oc delete --ignore-not-found -f - || true
for ns in "${ALL_NAMESPACES[@]}"; do
  oc delete secret seaweedfs-tls-custom -n "$ns" --ignore-not-found || true
  oc delete secret seaweedfs-tls-serving -n "$ns" --ignore-not-found || true
done
if oc get secret storage-config -n kserve-ci-e2e-test > /dev/null 2>&1; then
  oc patch secret storage-config -n kserve-ci-e2e-test --type=json \
    -p='[{"op": "remove", "path": "/data/localTLSS3Serving"}, {"op": "remove", "path": "/data/localTLSS3Custom"}]' 2>/dev/null || true
fi
rm -rf "$PROJECT_ROOT/test/scripts/openshift-ci/tls/certs"

# SeaweedFS backend deployed separately in operator mode
for ns in "${ALL_NAMESPACES[@]}"; do
  kustomize build "$PROJECT_ROOT/config/overlays/test/s3-local-backend" 2>/dev/null |
    sed "s/namespace: kserve/namespace: ${ns}/" |
    oc delete -n "$ns" --ignore-not-found -f - || true
done

# ---------------------------------------------------------------------------
# 5. ODH Model Controller
# ---------------------------------------------------------------------------
echo "Deleting ODH Model Controller"
kustomize build "$PROJECT_ROOT/test/scripts/openshift-ci" 2>/dev/null |
  oc delete --ignore-not-found -f - || true
for ns in "${ALL_NAMESPACES[@]}"; do
  oc wait --for=delete pod -l app=odh-model-controller -n "$ns" --timeout=30s 2>/dev/null || true
done

# ---------------------------------------------------------------------------
# 6. CI namespace and ServingRuntimes
# ---------------------------------------------------------------------------
echo "Delete CI namespace and ServingRuntimes"
"$MY_PATH/teardown-ci-namespace.sh" "" "kserve-ci-e2e-test"

oc delete -f "$PROJECT_ROOT/config/overlays/test/s3-local-backend/mlpipeline-s3-artifact-secret.yaml" -n kserve-ci-e2e-test --ignore-not-found || true

kustomize build "$PROJECT_ROOT/config/overlays/test/clusterresources" 2>/dev/null |
  sed 's/ClusterServingRuntime/ServingRuntime/' |
  sed "s|kserve/sklearnserver:latest|${SKLEARN_IMAGE}|" |
  sed "s|kserve/storage-initializer:latest|${STORAGE_INITIALIZER_IMAGE}|" |
  oc delete -n kserve-ci-e2e-test --ignore-not-found -f - || true

# ---------------------------------------------------------------------------
# 7. NetworkPolicy (try every possible namespace)
# ---------------------------------------------------------------------------
echo "Deleting NetworkPolicy"
for ns in "${ALL_NAMESPACES[@]}"; do
  oc delete networkpolicy allow-all -n "$ns" --ignore-not-found || true
done

# ---------------------------------------------------------------------------
# 8. CMA / KEDA operator
# ---------------------------------------------------------------------------
echo "Delete CMA / KEDA operator"
oc delete kedacontroller -n openshift-keda keda --ignore-not-found || true
oc delete subscription -n openshift-keda openshift-custom-metrics-autoscaler-operator --ignore-not-found || true
oc delete namespace openshift-keda --ignore-not-found || true

# ---------------------------------------------------------------------------
# 9. Application namespaces (opendatahub / redhat-ods-applications)
#    The "kserve" namespace is only created by raw-mode setup, but clean it too.
# ---------------------------------------------------------------------------
echo "Deleting application namespaces"
for ns in "${ALL_NAMESPACES[@]}"; do
  oc delete namespace "$ns" --ignore-not-found --timeout=120s || true
done

echo "Teardown complete"
