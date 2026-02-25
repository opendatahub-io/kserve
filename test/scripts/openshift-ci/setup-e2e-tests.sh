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

# This is a helper script to run E2E tests on the openshift-ci operator.
# This script assumes to be run inside a container/machine that has
# python pre-installed and the `oc` command available. Additional tooling,
# like kustomize are installed by the script if not available.
# The oc CLI is assumed to be configured with the credentials of the
# target cluster. The target cluster is assumed to be a clean cluster.
set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/common.sh"
PROJECT_ROOT="$(find_project_root "$SCRIPT_DIR")"

readonly MARKERS="${1:-raw}"
readonly PARALLELISM="${2:-1}"

readonly DEPLOYMENT_PROFILE="${3:-serverless}"
validate_deployment_profile "${DEPLOYMENT_PROFILE}"

: "${NS:=opendatahub}"
: "${SKLEARN_IMAGE:=kserve/sklearnserver:latest}"
: "${KSERVE_CONTROLLER_IMAGE:=quay.io/opendatahub/kserve-controller:stable-2.x}"
: "${KSERVE_AGENT_IMAGE:=quay.io/opendatahub/kserve-agent:latest}"
: "${KSERVE_ROUTER_IMAGE:=quay.io/opendatahub/kserve-router:latest}"
: "${STORAGE_INITIALIZER_IMAGE:=quay.io/opendatahub/kserve-storage-initializer:latest}"
: "${ODH_MODEL_CONTROLLER_IMAGE:=quay.io/opendatahub/odh-model-controller:stable-2.x}"
: "${IMAGE_TRANSFORMER_IMG_TAG:=kserve/image-transformer}"
: "${ERROR_404_ISVC_IMAGE:=error-404-isvc:latest}"
: "${SUCCESS_200_ISVC_IMAGE:=success-200-isvc:latest}"

echo "NS=$NS"
echo "SKLEARN_IMAGE=$SKLEARN_IMAGE"
echo "KSERVE_CONTROLLER_IMAGE=$KSERVE_CONTROLLER_IMAGE"
echo "KSERVE_AGENT_IMAGE=$KSERVE_AGENT_IMAGE"
echo "KSERVE_ROUTER_IMAGE=$KSERVE_ROUTER_IMAGE"
echo "STORAGE_INITIALIZER_IMAGE=$STORAGE_INITIALIZER_IMAGE"
echo "ERROR_404_ISVC_IMAGE=$ERROR_404_ISVC_IMAGE"
echo "SUCCESS_200_ISVC_IMAGE=$SUCCESS_200_ISVC_IMAGE"
echo "ODH_MODEL_CONTROLLER_IMAGE=$ODH_MODEL_CONTROLLER_IMAGE"
echo "IMAGE_TRANSFORMER_IMG_TAG=$IMAGE_TRANSFORMER_IMG_TAG "

# Create directory for installing tooling
mkdir -p $HOME/.local/bin
export PATH="$HOME/.local/bin:$PATH"

# Force KServe client to use external hostnames instead of cluster IPs
export CI_USE_ISVC_HOST=1

# If Kustomize is not installed, install it
if ! command -v kustomize &>/dev/null; then
  echo "⏳ Installing Kustomize"
  curl -s "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh" | bash -s -- 5.7.1 $HOME/.local/bin
fi

echo "⏳ Installing KServe Python SDK ..."
pushd $PROJECT_ROOT >/dev/null
  ./test/scripts/gh-actions/setup-poetry.sh
popd
pushd $PROJECT_ROOT/python/kserve >/dev/null
  poetry install --with=test --no-interaction
popd

if [[ "${DEPLOYMENT_PROFILE}" == "raw" ]]; then 
  $SCRIPT_DIR/infra/deploy.cma.sh
  # Add CA certificate extraction for raw deployments
  echo "⏳ Extracting OpenShift CA certificates for raw deployment"
  # Get comprehensive CA bundle including both cluster and service CAs
  {
    # Cluster root CA bundle
    oc get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}' 2>/dev/null && echo ""

    # OpenShift service CA
    oc get configmap openshift-service-ca.crt -n openshift-config-managed -o jsonpath='{.data.service-ca\.crt}' 2>/dev/null || \
    oc get secret service-ca -n openshift-service-ca -o jsonpath='{.data.service-ca\.crt}' 2>/dev/null | base64 -d || true
  } > /tmp/ca.crt

  # Verify we got a valid CA bundle
  if [ -s "/tmp/ca.crt" ] && grep -q "BEGIN CERTIFICATE" "/tmp/ca.crt"; then
    echo "✅ CA certificate bundle extracted ($(grep -c "BEGIN CERTIFICATE" /tmp/ca.crt) certificates)"
  else
    echo "❌ Failed to extract CA certificates"
  fi
fi

# Install KServe stack
if [[ "${DEPLOYMENT_PROFILE}" == "serverless" ]]; then
  echo "⏳ Installing OSSM"
  $SCRIPT_DIR/infra/deploy.ossm.sh
  echo "⏳ Installing Serverless"
  $SCRIPT_DIR/infra/deploy.serverless.sh
fi

if [[ "${DEPLOYMENT_PROFILE}" == "llm-d" ]]; then
  echo "⏳ Installing llm-d prerequisites"
  $SCRIPT_DIR/setup-llm.sh --skip-kserve
fi

echo "⏳ Waiting for KServe CRDs"
kustomize build $PROJECT_ROOT/config/crd | oc apply --server-side=true -f -

wait_for_crd llminferenceserviceconfigs.serving.kserve.io 90s

echo "⏳ Installing KServe with SeaweedFS"
kustomize build $PROJECT_ROOT/config/overlays/odh-test |
  sed "s|kserve/storage-initializer:latest|${STORAGE_INITIALIZER_IMAGE}|" |
  sed "s|kserve/agent:latest|${KSERVE_AGENT_IMAGE}|" |
  sed "s|kserve/router:latest|${KSERVE_ROUTER_IMAGE}|" |
  sed "s|kserve/kserve-controller:latest|${KSERVE_CONTROLLER_IMAGE}|" |
  oc apply --server-side=true --force-conflicts -f -

wait_for_crd datascienceclusters.datasciencecluster.opendatahub.io 90s
wait_for_crd dscinitializations.dscinitialization.opendatahub.io 90s
             
oc create -f ${PROJECT_ROOT}/config/overlays/odh-test/dsci.yaml
oc create -f ${PROJECT_ROOT}/config/overlays/odh-test/dsc.yaml

export OPENSHIFT_INGRESS_DOMAIN=$(oc get ingresses.config cluster -o jsonpath='{.spec.domain}')

# Patch the inferenceservice-config ConfigMap, when running RawDeployment tests
if [[ "${MARKERS}" =~ raw || "${MARKERS}" =~ graph ]]; then
  echo "✅ Patching RAW, markers: ${MARKERS}"
  oc patch configmap inferenceservice-config -n ${NS} --patch-file <(cat ${PROJECT_ROOT}/config/overlays/odh-test/configmap/inferenceservice-openshift-ci-raw.yaml | envsubst)
  oc delete pod -n ${NS} -l control-plane=kserve-controller-manager

  oc patch DataScienceCluster test-dsc --type='json' -p='[{"op": "replace", "path": "/spec/components/kserve/defaultDeploymentMode", "value": "RawDeployment"}]'
fi

if [[ "${MARKERS}" == *"predictor"* || "${MARKERS}" == *"path"* ]]; then
    echo "✅ Patching Serverless, markers: ${MARKERS}"
    oc patch configmap inferenceservice-config -n ${NS} --patch-file <(cat ${PROJECT_ROOT}/config/overlays/odh-test/configmap/inferenceservice-openshift-ci-serverless.yaml | envsubst)
fi

if [[ "${DEPLOYMENT_PROFILE}" == "llm-d" ]]; then
  oc patch configmap inferenceservice-config -n ${NS} --patch-file <(cat ${PROJECT_ROOT}/config/overlays/odh-test/configmap/inferenceservice-openshift-ci-llm.yaml | envsubst)
fi

wait_for_pod_ready "${NS}" "control-plane=kserve-controller-manager"

if [ "${DEPLOYMENT_PROFILE}" == "serverless" ]; then
  echo "⏳ Installing authorino and kserve gateways"
  curl -sL https://raw.githubusercontent.com/Kuadrant/authorino-operator/main/utils/install.sh | sed "s|kubectl|oc|" | 
    bash -s -- -v 0.16.0
fi

# TODO can be moved to odh-test overlays
echo "⏳ Installing ODH Model Controller"
kustomize build $PROJECT_ROOT/test/scripts/openshift-ci |
    sed "s|quay.io/opendatahub/odh-model-controller:stable|${ODH_MODEL_CONTROLLER_IMAGE}|" |
    oc apply -n ${NS} -f -

wait_for_pod_ready "${NS}" "app=odh-model-controller"

echo "Add testing models to SeaweedFS S3 storage ..."
echo "⏳ Waiting for SeaweedFS deployment to be ready..."
oc rollout status deployment/seaweedfs -n ${NS} --timeout=300s

if oc wait --for=condition=complete job/s3-init -n ${NS} --timeout=60s 2>/dev/null; then
  echo "S3 init job already completed successfully"
else
  echo "S3 init job not completed, re-creating..."
  oc delete job s3-init -n ${NS} --wait=true --ignore-not-found
  sed "s/s3-service.kserve/s3-service.${NS}/" \
    "$PROJECT_ROOT/config/overlays/test/s3-local-backend/seaweedfs-init-job.yaml" | \
    oc apply -n ${NS} -f -

  echo "Waiting for S3 init job to complete..."
  if ! oc wait --for=condition=complete job/s3-init -n ${NS} --timeout=300s; then
    echo "S3 init job failed. Pod status and logs:"
    oc get pods -l job-name=s3-init -n ${NS}
    oc logs -l job-name=s3-init -n ${NS} --tail=50 || true
    exit 1
  fi
fi

if [[ "${MARKERS}" == *"kserve_on_openshift"* ]]; then
  echo "Configuring SeaweedFS S3 TLS"
  S3_NAMESPACE="${NS}" "$PROJECT_ROOT/test/scripts/openshift-ci/tls/setup-s3-tls.sh" custom
  S3_NAMESPACE="${NS}" "$PROJECT_ROOT/test/scripts/openshift-ci/tls/setup-s3-tls.sh" serving
fi

echo "Prepare CI namespace and install ServingRuntimes"
oc create ns kserve-ci-e2e-test || true

if [ "${DEPLOYMENT_PROFILE}" == "serverless" ]; then
  cat <<EOF | oc apply -f -
apiVersion: maistra.io/v1
kind: ServiceMeshMember
metadata:
  name: default
  namespace: kserve-ci-e2e-test
spec:
  controlPlaneRef:
    namespace: istio-system
    name: basic
EOF
fi

oc apply -n kserve-ci-e2e-test -f <(
  sed "s|http://s3-service\.kserve:8333|http://s3-service.${NS}:8333|g" \
      "$PROJECT_ROOT/config/overlays/test/s3-local-backend/storage-config-secret.yaml"
)

kustomize build $PROJECT_ROOT/config/overlays/odh-test/clusterresources |
  sed "s|kserve/sklearnserver:latest|${SKLEARN_IMAGE}|" |
  sed "s|kserve/storage-initializer:latest|${STORAGE_INITIALIZER_IMAGE}|" |
  oc apply -n kserve-ci-e2e-test -f -

# Add the enablePassthrough annotation to the ServingRuntimes, to let Knative to
# generate passthrough routes.
if [ "${DEPLOYMENT_PROFILE}" == "serverless" ]; then
  oc annotate servingruntimes -n kserve-ci-e2e-test --all serving.knative.openshift.io/enablePassthrough=true
fi

# Allow all traffic to the kserve namespace. Without this networkpolicy, webhook will return 500
# error msg: 'http: server gave HTTP response to HTTPS client"}]},"code":500}'
{
cat <<EOF | oc apply -f -
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-all
  namespace: ${NS}
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
} || true

echo "✅ Setup complete"
