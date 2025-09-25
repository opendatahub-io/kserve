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

# install_upstream_istio <project_root>
install_upstream_istio() {
  local PROJECT_ROOT="$1"

  echo "⚠️  Installing upstream Istio GIE support"
  echo "⚠️  Temporarily until Ingress Operator provides it out of the box"

  oc create namespace istio-system   >/dev/null 2>&1 || true
  oc create namespace openshift-ingress >/dev/null 2>&1 || true

  oc create -f "${PROJECT_ROOT}/test/overlays/llm-istio-experimental" -n istio-system || true

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
