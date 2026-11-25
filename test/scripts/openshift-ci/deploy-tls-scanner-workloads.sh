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

# Deploys durable TLS-enabled workloads for the TLS scanner. Unlike pytest
# fixtures, these remain in place until the scanner has completed.
set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

: "${TLS_SCANNER_NAMESPACE:=kserve-tls-scanner}"
: "${TLS_SCANNER_READY_TIMEOUT:=20m}"
: "${SKLEARN_IMAGE:=docker.io/kserve/sklearnserver:latest}"

readonly NAMESPACE_MARKER_KEY="tests.kserve.io/managed-by"
readonly NAMESPACE_MARKER_VALUE="kserve-tls-scanner"
readonly FIXTURE_LABEL="tests.kserve.io/tls-scanner-fixture=true"
readonly RUNTIME_LABEL="tests.kserve.io/tls-scanner-runtime=true"
readonly FIXTURE_DIR="$PROJECT_ROOT/test/overlays/openshift-ci/tls-scanner-workloads"
readonly ISVCS=(tls-scanner-sklearn-v1 tls-scanner-sklearn-v2)
readonly LLMISVCS=(tls-scanner-llm)
WAITED_PODS=()

command -v oc >/dev/null 2>&1 || {
  echo "The oc CLI is required to deploy TLS scanner workloads." >&2
  exit 1
}

echo "Preparing TLS scanner workload namespace: $TLS_SCANNER_NAMESPACE"
if oc get namespace "$TLS_SCANNER_NAMESPACE" >/dev/null 2>&1; then
  namespace_marker="$(oc get namespace "$TLS_SCANNER_NAMESPACE" \
    -o go-template="{{ index .metadata.labels \"$NAMESPACE_MARKER_KEY\" }}")"
  if [[ "$namespace_marker" != "$NAMESPACE_MARKER_VALUE" ]]; then
    echo "Refusing to use existing namespace '$TLS_SCANNER_NAMESPACE': expected label $NAMESPACE_MARKER_KEY=$NAMESPACE_MARKER_VALUE." >&2
    exit 1
  fi
else
  oc create namespace "$TLS_SCANNER_NAMESPACE"
  oc label namespace "$TLS_SCANNER_NAMESPACE" \
    "$NAMESPACE_MARKER_KEY=$NAMESPACE_MARKER_VALUE"
fi

echo "Installing the sklearn ServingRuntime using image: $SKLEARN_IMAGE"
sed \
  -e 's/^kind: ClusterServingRuntime$/kind: ServingRuntime/' \
  -e "s|image: kserve-sklearnserver:replace|image: ${SKLEARN_IMAGE}|" \
  "$PROJECT_ROOT/config/runtimes/kserve-sklearnserver.yaml" |
  oc apply -n "$TLS_SCANNER_NAMESPACE" -f -
oc label servingruntime/kserve-sklearnserver -n "$TLS_SCANNER_NAMESPACE" \
  "$RUNTIME_LABEL" --overwrite

echo "Creating TLS scanner fixtures in $TLS_SCANNER_NAMESPACE"
oc apply -n "$TLS_SCANNER_NAMESPACE" -f "$FIXTURE_DIR"

wait_for_pods() {
  local selector="$1"
  local workload="$2"

  WAITED_PODS=()
  echo "Waiting for $workload pods to become Ready (timeout: $TLS_SCANNER_READY_TIMEOUT)" >&2
  oc wait --for=condition=Ready --timeout="$TLS_SCANNER_READY_TIMEOUT" \
    pods -l "$selector" -n "$TLS_SCANNER_NAMESPACE" >/dev/null

  local pod_names
  pod_names="$(oc get pods -l "$selector" -n "$TLS_SCANNER_NAMESPACE" \
    -o jsonpath='{.items[*].metadata.name}')"
  if [[ -z "$pod_names" ]]; then
    echo "$workload is Ready but has no serving pods." >&2
    return 1
  fi

  read -r -a WAITED_PODS <<< "$pod_names"
}

for isvc in "${ISVCS[@]}"; do
  echo "Waiting for InferenceService/$isvc to become Ready (timeout: $TLS_SCANNER_READY_TIMEOUT)"
  oc wait --for=condition=Ready --timeout="$TLS_SCANNER_READY_TIMEOUT" \
    "inferenceservice/$isvc" -n "$TLS_SCANNER_NAMESPACE"

  selector="serving.kserve.io/inferenceservice=$isvc"
  wait_for_pods "$selector" "InferenceService/$isvc"
  for pod in "${WAITED_PODS[@]}"; do
    containers="$(oc get "pod/$pod" --ignore-not-found -n "$TLS_SCANNER_NAMESPACE" \
      -o jsonpath='{.spec.containers[*].name}')"
    if [[ -z "$containers" ]]; then
      echo "Pod $pod was replaced during sidecar verification; rechecking workload readiness." >&2
      continue
    fi
    if [[ " $containers " != *" kube-rbac-proxy "* ]]; then
      echo "Serving pod $pod does not contain kube-rbac-proxy; scanner fixture is not exercising the authenticated TLS path." >&2
      exit 1
    fi
  done
  oc wait --for=condition=Ready --timeout="$TLS_SCANNER_READY_TIMEOUT" \
    pods -l "$selector" -n "$TLS_SCANNER_NAMESPACE" >/dev/null

  oc get inferenceservice "$isvc" -n "$TLS_SCANNER_NAMESPACE" -o wide
  oc get pods -l "$selector" -n "$TLS_SCANNER_NAMESPACE" -o wide
done

for llmisvc in "${LLMISVCS[@]}"; do
  echo "Waiting for LLMInferenceService/$llmisvc to become Ready (timeout: $TLS_SCANNER_READY_TIMEOUT)"
  oc wait --for=condition=Ready --timeout="$TLS_SCANNER_READY_TIMEOUT" \
    "llminferenceservice/$llmisvc" -n "$TLS_SCANNER_NAMESPACE"

  selector="app.kubernetes.io/name=$llmisvc"
  wait_for_pods "$selector" "LLMInferenceService/$llmisvc"
  for pod in "${WAITED_PODS[@]}"; do
    containers="$(oc get "pod/$pod" --ignore-not-found -n "$TLS_SCANNER_NAMESPACE" \
      -o jsonpath='{.spec.containers[*].name}')"
    if [[ -z "$containers" ]]; then
      echo "Pod $pod was replaced during TLS verification; rechecking workload readiness." >&2
      continue
    fi
    args="$(oc get "pod/$pod" --ignore-not-found -n "$TLS_SCANNER_NAMESPACE" \
      -o jsonpath='{range .spec.containers[*]}{.args[*]}{" "}{end}')"
    if [[ " $containers " != *" main "* || "$args" != *"--ssl-certfile"* || "$args" != *"--ssl-keyfile"* ]]; then
      echo "LLMInferenceService pod $pod is not configured for native TLS; verify enableLLMInferenceServiceTLS and the rendered simulator arguments." >&2
      exit 1
    fi
  done
  oc wait --for=condition=Ready --timeout="$TLS_SCANNER_READY_TIMEOUT" \
    pods -l "$selector" -n "$TLS_SCANNER_NAMESPACE" >/dev/null

  oc get llminferenceservice "$llmisvc" -n "$TLS_SCANNER_NAMESPACE" -o wide
  oc get pods -l "$selector" -n "$TLS_SCANNER_NAMESPACE" -o wide
done

echo "TLS scanner workloads are deployed and healthy in $TLS_SCANNER_NAMESPACE."
