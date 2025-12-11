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

# This script sets up the kserve-ci-e2e-test namespace for E2E testing.
# It is idempotent - it will delete and recreate the namespace if it already exists.
set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "$SCRIPT_DIR/common.sh"

# Helper function to add storage-config entry for TLS MinIO
add_storage_config_for_tls_minio() {
  local cert_type="$1"
  local storage_config_key
  local service_name
  
  if [[ "$cert_type" == "serving" ]]; then
    storage_config_key="localTLSMinIOServing"
    service_name="minio-tls-serving-service"
  elif [[ "$cert_type" == "custom" ]]; then
    storage_config_key="localTLSMinIOCustom"
    service_name="minio-tls-custom-service"
  else
    echo "Error: Invalid certificate type: $cert_type"
    return 1
  fi
  
  echo "Adding $storage_config_key configuration to storage-config secret"
  local local_tls_minio="{\"type\": \"s3\",\"access_key_id\":\"minio\",\"secret_access_key\":\"minio123\",\"endpoint_url\":\"https://${service_name}.kserve.svc:9000\",\"bucket\":\"mlpipeline\",\"region\":\"us-south\",\"cabundle_configmap\":\"odh-kserve-custom-ca-bundle\",\"anonymous\":\"False\"}"
  local local_tls_minio_base64
  local_tls_minio_base64=$(echo "${local_tls_minio}" | base64 -w 0)
  
  if oc get secret storage-config -n "$NAMESPACE" >/dev/null 2>&1; then
    oc patch secret storage-config -n "$NAMESPACE" -p "{\"data\":{\"${storage_config_key}\":\"${local_tls_minio_base64}\"}}"
  else
    oc create secret generic storage-config --from-literal="${storage_config_key}=${local_tls_minio}" -n "$NAMESPACE"
  fi
}

# Get deployment type from first argument, default to empty string
DEPLOYMENT_TYPE="${1:-}"

# Image variables with defaults (will use environment variables if set)
: "${SKLEARN_IMAGE:=kserve/sklearnserver:latest}"
: "${STORAGE_INITIALIZER_IMAGE:=quay.io/opendatahub/kserve-storage-initializer:latest}"

NAMESPACE="kserve-ci-e2e-test"

echo "Setting up CI namespace: $NAMESPACE"

# Delete namespace if it exists for idempotency
"$SCRIPT_DIR/teardown-ci-namespace.sh" "$DEPLOYMENT_TYPE"

# Create namespace
echo "Creating namespace $NAMESPACE"
cat <<EOF | oc apply -f -
apiVersion: v1
kind: Namespace
metadata:
  name: $NAMESPACE
EOF

# Add ServiceMeshMember if not skipping serverless
if ! skip_serverless "$DEPLOYMENT_TYPE"; then
  echo "Adding ServiceMeshMember to namespace"
  cat <<EOF | oc apply -f -
apiVersion: maistra.io/v1
kind: ServiceMeshMember
metadata:
  name: default
  namespace: $NAMESPACE
spec:
  controlPlaneRef:
    namespace: istio-system
    name: basic
EOF
fi

# Apply minio user secret
echo "Applying minio user secret"
oc apply -f "$PROJECT_ROOT/config/overlays/test/minio/minio-user-secret.yaml" -n "$NAMESPACE"

# Build and apply ServingRuntimes
echo "Installing ServingRuntimes"
kustomize build "$PROJECT_ROOT/config/overlays/test/clusterresources" |
  sed 's/ClusterServingRuntime/ServingRuntime/' |
  sed "s|kserve/sklearnserver:latest|${SKLEARN_IMAGE}|" |
  sed "s|kserve/storage-initializer:latest|${STORAGE_INITIALIZER_IMAGE}|" |
  oc apply -n "$NAMESPACE" -f -

# Add the enablePassthrough annotation to the ServingRuntimes, to let Knative to
# generate passthrough routes.
if ! skip_serverless "$DEPLOYMENT_TYPE"; then
  echo "Annotating ServingRuntimes with enablePassthrough"
  # Check if any servingruntimes exist before annotating
  if oc get servingruntimes -n "$NAMESPACE" --no-headers 2>/dev/null | grep -q .; then
    oc annotate servingruntimes -n "$NAMESPACE" --all serving.knative.openshift.io/enablePassthrough=true --overwrite
  else
    echo "Warning: No ServingRuntimes found to annotate"
  fi
fi

# Configure namespace-specific minio TLS storage-config if needed
if [[ "$DEPLOYMENT_TYPE" =~ "kserve_on_openshift" ]]; then
  echo "Configuring namespace-specific minio TLS storage-config"
  add_storage_config_for_tls_minio custom
  add_storage_config_for_tls_minio serving
fi

echo "CI namespace setup complete"

