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

# Sets up the full E2E test environment on OpenShift:
#   1. Optionally builds and pushes local KServe images (RUNNING_LOCAL=true)
#   2. Installs KServe on the cluster via setup-kserve.sh
#   3. Deploys E2E test infrastructure (SeaweedFS S3 backend, CA certs, ServingRuntimes)
#
# Assumes: python pre-installed, oc CLI configured against the target cluster.
set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$SCRIPT_DIR/common.sh"
source "$SCRIPT_DIR/version.sh"

# Always print cluster / operator snapshot on exit (success or failure) for CI triage.
trap print_e2e_environment_summary EXIT

# When running locally (not in CI), build test images before cluster setup
: "${RUNNING_LOCAL:=false}"
: "${BUILD_KSERVE_IMAGES:=true}"
: "${BUILD_GRAPH_IMAGES:=true}"

export IMAGE_TRANSFORMER_IMG_TAG="${IMAGE_TRANSFORMER_IMG_TAG:-kserve/image-transformer:latest}"

if [[ "$RUNNING_LOCAL" == "true" ]]; then
  export CUSTOM_MODEL_GRPC_IMG_TAG=kserve/custom-model-grpc:latest
  export GITHUB_SHA=master

  if [[ "$BUILD_KSERVE_IMAGES" == "true" ]]; then
    echo "Building KServe test images..."
    source "$PROJECT_ROOT/test/scripts/openshift-ci/build-kserve-images.sh" \
      > >(tee "$PROJECT_ROOT/test/scripts/openshift-ci/build-kserve-images.log") 2>&1
  fi

  if [[ "$1" == "graph" ]] && [[ "$BUILD_GRAPH_IMAGES" == "true" ]]; then
    echo "Building graph test images..."
    "$PROJECT_ROOT/test/scripts/gh-actions/build-graph-tests-images.sh" \
      > >(tee "$PROJECT_ROOT/test/scripts/openshift-ci/build-graph-tests-images.log") 2>&1
  fi
fi

# Derive install mode from OPERATOR_TYPE (empty = manual kustomize deploy).
# KSERVE_NAMESPACE and SEAWEEDFS_BUNDLED are exported so they are available
# after setup-kserve.sh returns (subprocess exports cannot propagate to this shell).
: "${OPERATOR_TYPE:=}"
case "${OPERATOR_TYPE}" in
  rhods|rhoai)       export KSERVE_NAMESPACE="redhat-ods-applications"; export SEAWEEDFS_BUNDLED=false ;;
  odh|opendatahub)   export KSERVE_NAMESPACE="opendatahub";             export SEAWEEDFS_BUNDLED=false ;;
  "")                export KSERVE_NAMESPACE="kserve";                   export SEAWEEDFS_BUNDLED=true ;;
  *)                 echo "Error: Unknown OPERATOR_TYPE '${OPERATOR_TYPE}'"; exit 1 ;;
esac

echo "Using namespace: $KSERVE_NAMESPACE for KServe components"

# Test-only image defaults (used by pytest, not by the KServe deployment itself)
: "${SKLEARN_IMAGE:=kserve/sklearnserver:latest}"
: "${ERROR_404_ISVC_IMAGE:=error-404-isvc:latest}"
: "${SUCCESS_200_ISVC_IMAGE:=success-200-isvc:latest}"
: "${STORAGE_INITIALIZER_IMAGE:=quay.io/opendatahub/kserve-storage-initializer:latest}"

: "${OPT_125M_MODEL_URI:=s3://example-models/facebook/opt-125m}"
export OPT_125M_MODEL_URI

echo "SKLEARN_IMAGE=$SKLEARN_IMAGE"
echo "OPT_125M_MODEL_URI=$OPT_125M_MODEL_URI"
echo "ERROR_404_ISVC_IMAGE=$ERROR_404_ISVC_IMAGE"
echo "SUCCESS_200_ISVC_IMAGE=$SUCCESS_200_ISVC_IMAGE"

# Install kustomize and yq (also needed for SeaweedFS kustomize build below)
$PROJECT_ROOT/hack/setup/cli/install-kustomize.sh
make -C "$PROJECT_ROOT" yq
export PATH="${PROJECT_ROOT}/bin:${PATH}"

echo "Installing KServe Python SDK ..."
pushd $PROJECT_ROOT >/dev/null
  ./test/scripts/gh-actions/setup-uv.sh
  export PATH="${PROJECT_ROOT}/bin:${PATH}"
popd
pushd $PROJECT_ROOT/python/kserve >/dev/null
  uv sync --active --group test
  uv pip install timeout-sampler
popd

# Install KServe on the cluster (dispatch to the correct install method + common post-install)
"$SCRIPT_DIR/setup-kserve.sh" "$1"

# Configure CA cert for Python requests.
# The run-e2e-tests script expects the CA cert at /tmp/ca.crt.
# This overwrites the early raw-deployment cert written by setup-kserve.sh with the
# definitive bundle sourced from KSERVE_NAMESPACE (namespace-scoped service CA).
{
  oc get configmap kube-root-ca.crt -n "${KSERVE_NAMESPACE}" -o jsonpath='{.data.ca\.crt}'
  echo ""
  oc get configmap openshift-service-ca.crt -n "${KSERVE_NAMESPACE}" \
    -o jsonpath='{.data.service-ca\.crt}' 2>/dev/null || true
} > /tmp/ca.crt

echo "Add testing models to SeaweedFS S3 storage ..."

# In manual mode SeaweedFS is bundled in the kustomize overlay and is already deployed.
# For operator and kserve-module installs it must be deployed separately.
if [[ "$SEAWEEDFS_BUNDLED" == "false" ]]; then
  echo "Deploying SeaweedFS S3 backend for tests..."
  kustomize build "$PROJECT_ROOT/config/overlays/test/s3-local-backend" | \
    sed "s/namespace: kserve/namespace: ${KSERVE_NAMESPACE}/" | \
    oc apply -n ${KSERVE_NAMESPACE} -f -
fi

echo "Waiting for SeaweedFS deployment to be ready..."
oc rollout status deployment/seaweedfs -n ${KSERVE_NAMESPACE} --timeout=300s

# The s3-init job is already created by the kustomize build above.
# It may have failed if SeaweedFS wasn't ready yet, so check and re-create if needed.
if oc wait --for=condition=complete job/s3-init -n ${KSERVE_NAMESPACE} --timeout=60s 2>/dev/null; then
  echo "S3 init job already completed successfully"
else
  echo "S3 init job not completed, re-creating..."
  sed "s/s3-service.kserve/s3-service.${KSERVE_NAMESPACE}/" \
    "$PROJECT_ROOT/test/overlays/openshift-ci/seaweedfs-init-job-odh.yaml" | \
    sed "s|kserve/storage-initializer:latest|${STORAGE_INITIALIZER_IMAGE}|" | \
    oc replace --force -n ${KSERVE_NAMESPACE} -f -

  echo "Waiting for S3 init job to complete..."
  if ! oc wait --for=condition=complete job/s3-init -n ${KSERVE_NAMESPACE} --timeout=300s; then
    echo "S3 init job failed. Pod status and logs:"
    oc get pods -l job-name=s3-init -n ${KSERVE_NAMESPACE}
    oc logs -l job-name=s3-init -n ${KSERVE_NAMESPACE} --tail=50 || true
    exit 1
  fi
fi

# Configure S3 TLS if needed
if [[ "$1" =~ "kserve_on_openshift" ]]; then
  echo "Configuring SeaweedFS S3 TLS"
  "$PROJECT_ROOT/test/scripts/openshift-ci/tls/setup-s3-tls.sh" custom
  "$PROJECT_ROOT/test/scripts/openshift-ci/tls/setup-s3-tls.sh" serving
fi

echo "Prepare CI namespace and install ServingRuntimes"
$SCRIPT_DIR/setup-ci-namespace.sh "$1"

echo "Setup complete"
