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
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$SCRIPT_DIR/common.sh"

# Parse command line options
: "${INSTALL_ODH_OPERATOR:=false}"

# Set the applications namespace based on installation method
# ODH operator uses 'opendatahub', manual installation uses 'kserve'
if [[ "$INSTALL_ODH_OPERATOR" == "true" ]]; then
  KSERVE_NAMESPACE="opendatahub"
else
  KSERVE_NAMESPACE="kserve"
fi

echo "Using namespace: $KSERVE_NAMESPACE for KServe components"
: "${SKLEARN_IMAGE:=kserve/sklearnserver:latest}"
: "${KSERVE_CONTROLLER_IMAGE:=quay.io/opendatahub/kserve-controller:latest}"
: "${KSERVE_AGENT_IMAGE:=quay.io/opendatahub/kserve-agent:latest}"
: "${KSERVE_ROUTER_IMAGE:=quay.io/opendatahub/kserve-router:latest}"
: "${STORAGE_INITIALIZER_IMAGE:=quay.io/opendatahub/kserve-storage-initializer:latest}"
: "${ODH_MODEL_CONTROLLER_IMAGE:=quay.io/opendatahub/odh-model-controller:fast}"
: "${ERROR_404_ISVC_IMAGE:=error-404-isvc:latest}"
: "${SUCCESS_200_ISVC_IMAGE:=success-200-isvc:latest}"
: "${LLMISVC_CONTROLLER_IMAGE:=quay.io/opendatahub/llmisvc-controller:latest}"

echo "SKLEARN_IMAGE=$SKLEARN_IMAGE"
echo "KSERVE_CONTROLLER_IMAGE=$KSERVE_CONTROLLER_IMAGE"
echo "LLMISVC_CONTROLLER_IMAGE=$LLMISVC_CONTROLLER_IMAGE"
echo "KSERVE_AGENT_IMAGE=$KSERVE_AGENT_IMAGE"
echo "KSERVE_ROUTER_IMAGE=$KSERVE_ROUTER_IMAGE"
echo "STORAGE_INITIALIZER_IMAGE=$STORAGE_INITIALIZER_IMAGE"
echo "ERROR_404_ISVC_IMAGE=$ERROR_404_ISVC_IMAGE"
echo "SUCCESS_200_ISVC_IMAGE=$SUCCESS_200_ISVC_IMAGE"

readonly MARKERS="${1:-raw}"
readonly PARALLELISM="${2:-1}"
readonly DEPLOYMENT_PROFILE="${3:-serverless}"

if [[ "${MARKERS}" == *"llminferenceservice"* || "${MARKERS}" == *"llm-inference-service"* ]]; then
  echo "dummy stub for llm-inference-service setup"
  exit 0
fi

# Create directory for installing tooling
# It is assumed that $HOME/.local/bin is in the $PATH
mkdir -p $HOME/.local/bin
MY_PATH=$(dirname "$0")
PROJECT_ROOT=$MY_PATH/../../../

# If Kustomize is not installed, install it
if ! command -v kustomize &>/dev/null; then
  echo "Installing Kustomize"
  curl -s "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh" | bash -s -- 5.0.1 $HOME/.local/bin
fi

# If minio CLI is not installed, install it
if ! command -v mc &>/dev/null; then
  echo "Installing Minio CLI"
  curl https://dl.min.io/client/mc/release/linux-amd64/mc --create-dirs -o $HOME/.local/bin/mc
  chmod +x $HOME/.local/bin/mc
fi

echo "Installing KServe Python SDK ..."
pushd $PROJECT_ROOT >/dev/null
  ./test/scripts/gh-actions/setup-uv.sh
  # Add bin directory to PATH so uv command is available
  export PATH="${PROJECT_ROOT}/bin:${PATH}"
popd
pushd $PROJECT_ROOT/python/kserve >/dev/null
  uv sync --active --group test
  uv pip install timeout-sampler
popd

$MY_PATH/deploy.cma.sh

# Install KServe stack
if [ "${DEPLOYMENT_PROFILE}" == "serverless" ]; then
  echo "Installing OSSM"
  $MY_PATH/deploy.ossm.sh
  echo "Installing Serverless"
  $MY_PATH/deploy.serverless.sh
fi

# Install ODH operator if requested
if [[ "$INSTALL_ODH_OPERATOR" == "true" ]]; then
  $SCRIPT_DIR/deploy.odh.sh
fi

# Add CA certificate extraction for raw deployments
if [[ "$1" =~ raw ]]; then
  echo "⏳ Extracting OpenShift CA certificates for raw deployment"
  # Get comprehensive CA bundle including both cluster and service CAs
  {
    # Cluster root CA bundle
    oc get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}' 2>/dev/null && echo ""

export OPENSHIFT_INGRESS_DOMAIN=$(oc get ingresses.config cluster -o jsonpath='{.spec.domain}')

# Patch the inferenceservice-config ConfigMap, when running RawDeployment tests
if [[ "${MARKERS}" == *"raw" ]]; then
  oc patch configmap inferenceservice-config -n kserve --patch-file <(cat config/overlays/test/configmap/inferenceservice-openshift-ci-raw.yaml | envsubst)
  oc delete pod -n kserve -l control-plane=kserve-controller-manager

  oc patch DataScienceCluster test-dsc --type='json' -p='[{"op": "replace", "path": "/spec/components/kserve/defaultDeploymentMode", "value": "RawDeployment"}]'
fi

if [[ "${MARKERS}" == *"graph"* ]]; then
    oc patch configmap inferenceservice-config -n kserve --patch-file <(cat config/overlays/test/configmap/inferenceservice-openshift-ci-serverless.yaml | envsubst)
  else 
    oc patch configmap inferenceservice-config -n kserve --patch-file <(cat config/overlays/test/configmap/inferenceservice-openshift-ci-serverless-predictor.yaml | envsubst)
fi

oc wait --for=condition=ready pod -l control-plane=kserve-controller-manager -n kserve --timeout=300s

if [ "${DEPLOYMENT_PROFILE}" == "serverless" ]; then
  echo "Installing authorino and kserve gateways"
  curl -sL https://raw.githubusercontent.com/Kuadrant/authorino-operator/main/utils/install.sh | sed "s|kubectl|oc|" | 
    bash -s -- -v 0.16.0

fi

# Wait for/Install ODH Model Controller based on method
if [[ "$INSTALL_ODH_OPERATOR" == "false" ]]; then
  echo "Installing ODH Model Controller manually with PR images"
  kustomize build $PROJECT_ROOT/test/scripts/openshift-ci |
      sed "s|quay.io/opendatahub/odh-model-controller:fast|${ODH_MODEL_CONTROLLER_IMAGE}|" |
      oc apply -n ${KSERVE_NAMESPACE} -f -
  oc rollout status deployment/odh-model-controller -n ${KSERVE_NAMESPACE} --timeout=300s
else
  # ODH operator deploys odh-model-controller using custom manifests from PVC
  # The image was already configured in copy-kserve-manifests-to-pvc.sh via params.env
  echo "Waiting for ODH operator to deploy ODH Model Controller with PR image..."
  wait_for_pod_ready "${KSERVE_NAMESPACE}" "app=odh-model-controller" 600s

  echo "Verifying ODH Model Controller deployment..."
  oc rollout status deployment/odh-model-controller -n ${KSERVE_NAMESPACE} --timeout=300s

  # Verify the correct image is being used
  ACTUAL_IMAGE=$(oc get deployment odh-model-controller -n ${KSERVE_NAMESPACE} -o jsonpath='{.spec.template.spec.containers[0].image}')
  echo "ODH Model Controller deployed with image: $ACTUAL_IMAGE"
fi

# Configure certs for the python requests by getting the CA cert from the kserve controller pod
export CA_CERT_PATH="/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
# The run-e2e-tests script expects the CA cert to be in /tmp/ca.crt
oc exec deploy/kserve-controller-manager -n ${KSERVE_NAMESPACE} -- cat $CA_CERT_PATH > /tmp/ca.crt

echo "Add testing models to SeaweedFS S3 storage ..."

# Wait for SeaweedFS deployment to be ready
echo "Waiting for SeaweedFS deployment to be ready..."
oc rollout status deployment/seaweedfs -n ${KSERVE_NAMESPACE} --timeout=300s

# The s3-init job is already created by the kustomize build above.
# It may have failed if SeaweedFS wasn't ready yet, so check and re-create if needed.
if oc wait --for=condition=complete job/s3-init -n ${KSERVE_NAMESPACE} --timeout=60s 2>/dev/null; then
  echo "S3 init job already completed successfully"
else
  echo "S3 init job not completed, re-creating..."
  oc delete job s3-init -n ${KSERVE_NAMESPACE} --wait=true --ignore-not-found
  sed "s/s3-service.kserve/s3-service.${KSERVE_NAMESPACE}/" \
    "$PROJECT_ROOT/config/overlays/test/s3-local-backend/seaweedfs-init-job.yaml" | \
    oc apply -n ${KSERVE_NAMESPACE} -f -

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

# Allow all traffic to the KServe namespace. Without this networkpolicy, webhook will return 500
oc delete route -n kserve minio-service

echo "Prepare CI namespace and install ServingRuntimes"
cat <<EOF | oc apply -f -
apiVersion: v1
kind: Namespace
metadata:
  name: kserve-ci-e2e-test
EOF

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

oc apply -f $PROJECT_ROOT/config/overlays/test/minio/minio-user-secret.yaml -n kserve-ci-e2e-test

kustomize build $PROJECT_ROOT/config/overlays/test/clusterresources |
  sed 's/ClusterServingRuntime/ServingRuntime/' |
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

echo "Prepare CI namespace and install ServingRuntimes"
$SCRIPT_DIR/setup-ci-namespace.sh "$1"

echo "Setup complete"
