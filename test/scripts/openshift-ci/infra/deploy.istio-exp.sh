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

set -euo pipefail

GATEWAY_API_EXT_VERSION="v1.0.0"

# install_upstream_istio <project_root>
install_upstream_istio() {
  local PROJECT_ROOT="$1"

  echo "⚠️  Installing upstream Istio GIE support"
  echo "⚠️  Temporarily until Ingress Operator provides it out of the box"

  # OpenShift 4.19.9+ has Gateway API CRDs managed by Ingress Operator
  # Skip Gateway API CRD installation - they're already present and protected by admission policy
  echo "ℹ️  Using OpenShift-managed Gateway API CRDs (GatewayClass, Gateway, HTTPRoute, etc.)"

  # Install Gateway API Inference Extension CRDs only (InferencePool, InferenceModel, etc.)
  echo "📦 Installing Gateway API Inference Extension CRDs..."
  oc apply -f "https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/${GATEWAY_API_EXT_VERSION}/manifests.yaml"

  oc create namespace istio-system   >/dev/null 2>&1 || true
  oc create namespace openshift-ingress >/dev/null 2>&1 || true

  # Install Istio with GIE support
  echo "📦 Installing Istio with GIE support..."
  oc create -f "${PROJECT_ROOT}/test/overlays/llm-istio-experimental" -n istio-system || true

  # Wait for Istio to be ready
  echo "⏳ Waiting for Istio pods to be ready..."
  oc wait --for=condition=Ready pods --all --timeout=240s -n istio-system || true

  # Create GatewayClass for Istio controller
  echo "📦 Creating Istio GatewayClass..."
  {
    oc apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: istio
spec:
  controllerName: istio.io/gateway-controller
EOF
  } || true

  # Create Gateway with Istio controller
  echo "📦 Creating Istio Gateway..."
  {
    oc apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: openshift-ai-inference
  namespace: openshift-ingress
spec:
  gatewayClassName: istio
  listeners:
    - name: http
      port: 80
      protocol: HTTP
      allowedRoutes:
        namespaces:
          from: All
  infrastructure:
    labels:
      serving.kserve.io/gateway: kserve-ingress-gateway
EOF
  } || true

  echo "✅  Upstream Istio GIE support installed"
}
