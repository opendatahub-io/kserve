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

# This is a helper script to create and configure the resources needed
# for minio storage to have tls enabled with a custom certificate.
set -o errexit
set -o nounset
set -o pipefail

MY_PATH=$(dirname "$0")
PROJECT_ROOT=$MY_PATH/../../../../
TLS_DIR=$PROJECT_ROOT/test/scripts/openshift-ci/tls

: "${NS:=opendatahub}"

echo "NS=$NS"

# If Kustomize is not installed, install it
if ! command -v kustomize &>/dev/null; then
  echo "Installing Kustomize"
  curl -s "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh" | bash -s -- 5.0.1 $HOME/.local/bin
fi

# If minio CLI is not installed, install it
if ! command -v mc &>/dev/null; then
  echo "Installing MinIO CLI"
  curl https://dl.min.io/client/mc/release/linux-amd64/mc --create-dirs -o $HOME/.local/bin/mc
  chmod +x $HOME/.local/bin/mc
fi

# Create namespace if it does not already exist
if oc get namespace ${NS} > /dev/null 2>&1; then
    echo "Namespace ${NS} exists."
else
    cat <<EOF | oc apply -f -
apiVersion: v1
kind: Namespace
metadata:
    name: ${NS}
EOF
fi

# Required for idempotency
if oc get deployment minio-tls-custom -n ${NS} > /dev/null 2>&1; then
    echo "Cleaning up existing minio-tls-custom deployment"
    oc delete deployment minio-tls-custom -n ${NS}
fi

# Create tls minio resources
kustomize build $PROJECT_ROOT/test/overlays/openshift-ci/minio-tls-custom-cert |
  oc apply -n ${NS} --server-side=true -f -

# Wait for minio pod to be ready
echo "Waiting for minio-tls-custom pod to be ready..."
echo "Custom TLS MinIO oc get events"
oc get events
oc wait --for=condition=ready pod -l app=minio-tls-custom -n ${NS} --timeout=300s

echo "Configuring MinIO for TLS with custom certificate and adding models to storage ..."
# Create custom certs
${PROJECT_ROOT}/test/scripts/openshift-ci/tls/generate-custom-certs.sh
# Generate secret to store the custom certs. If the secret already exists, replace it.
if oc get secret minio-tls-custom -n ${NS} > /dev/null 2>&1; then
    oc delete secret minio-tls-custom -n ${NS}
fi
oc create secret generic minio-tls-custom --from-file=${TLS_DIR}/certs/custom/root.crt  --from-file=${TLS_DIR}/certs/custom/custom.crt --from-file=${TLS_DIR}/certs/custom/custom.key -n ${NS}
# Mount certificates to minio-tls-custom container
oc patch deployment minio-tls-custom -n ${NS} -p '{"spec":{"template":{"spec":{"containers":[{"name":"minio-tls-custom","volumeMounts":[{"mountPath":".minio/certs","name":"minio-tls-custom"}]}], "volumes":[{"name":"minio-tls-custom","projected":{"defaultMode":420,"sources":[{"secret":{"name":"minio-tls-custom","items":[{"key":"custom.crt","path":"public.crt"},{"key":"custom.key", "path":"private.key"},{"key":"root.crt","path":"CAs/root.crt"}]}}]}}]}}}}'

# Wait for patched deployment to be ready
echo "Waiting for patched minio-tls-custom deployment to be ready..."
oc rollout status deployment/minio-tls-custom -n ${NS} --timeout=300s

# Expose the route with tls enabled
oc create route reencrypt minio-tls-custom-service \
  --service=minio-tls-custom-service \
  --dest-ca-cert="${TLS_DIR}/certs/custom/root.crt" \
  -n ${NS} && sleep 15
MINIO_TLS_CUSTOM_ROUTE=$(oc get routes -n ${NS} minio-tls-custom-service -o jsonpath="{.spec.host}")
echo "Custom TLS MinIO route: $MINIO_TLS_CUSTOM_ROUTE"

# Wait for custom TLS minio endpoint to be accessible
echo "Waiting for custom TLS minio endpoint to be accessible..."
timeout=60
counter=0
while [ $counter -lt $timeout ]; do
  if curl -f -s -k "https://$MINIO_TLS_CUSTOM_ROUTE/minio/health/live" >/dev/null 2>&1; then
    echo "Custom TLS Minio is ready!"
    break
  fi
  echo "Waiting for custom TLS minio to be ready... ($counter/$timeout)"
  sleep 2
  counter=$((counter + 2))
done

if [ $counter -ge $timeout ]; then
  echo "Timeout waiting for custom TLS minio to be ready"
  exit 1
fi

# Upload the model
mc alias set storage-tls-custom https://$MINIO_TLS_CUSTOM_ROUTE minio minio123 --insecure
if ! mc ls storage-tls-custom/example-models --insecure >/dev/null 2>&1; then
  mc mb storage-tls-custom/example-models --insecure
else
  echo "Bucket 'example-models' already exists."
fi
if [[ $(mc ls storage-tls-custom/example-models/sklearn/model.joblib --insecure |wc -l) == "1" ]]; then
  echo "Test model exists"
else
  echo "Copy test model"
  curl -L https://storage.googleapis.com/kfserving-examples/models/sklearn/1.0/model/model.joblib -o /tmp/sklearn-model.joblib
  mc cp /tmp/sklearn-model.joblib storage-tls-custom/example-models/sklearn/model.joblib --insecure
fi
# Delete the route after upload
oc delete route -n ${NS} minio-tls-custom-service

# Create kserve-ci-e2e-test namespace if it does not already exist
if oc get namespace kserve-ci-e2e-test > /dev/null 2>&1; then
    echo "Namespace kserve-ci-e2e-test exists."
else
    cat <<EOF | oc apply -f -
apiVersion: v1
kind: Namespace
metadata:
    name: kserve-ci-e2e-test
EOF
fi

echo "Adding localTLSMinIOCustom configuration to storage-config secret"
# Creating/Updating storage-config secret with ca created ca bundle
LOCAL_TLS_MINIO_CUSTOM="{\"type\": \"s3\",\"access_key_id\":\"minio\",\"secret_access_key\":\"minio123\",\"endpoint_url\":\"https://minio-tls-custom-service.${NS}.svc:9000\",\"bucket\":\"mlpipeline\",\"region\":\"us-south\",\"cabundle_configmap\":\"odh-kserve-custom-ca-bundle\",\"anonymous\":\"False\"}" 
LOCAL_TLS_MINIO_CUSTOM_BASE64=$(echo ${LOCAL_TLS_MINIO_CUSTOM} | base64 -w 0)
if oc get secret storage-config -n kserve-ci-e2e-test > /dev/null 2>&1; then
    oc patch secret storage-config -n kserve-ci-e2e-test -p "{\"data\":{\"localTLSMinIOCustom\":\"${LOCAL_TLS_MINIO_CUSTOM_BASE64}\"}}"
else
    oc create secret generic storage-config --from-literal=localTLSMinIOCustom="${LOCAL_TLS_MINIO_CUSTOM}" -n kserve-ci-e2e-test
fi