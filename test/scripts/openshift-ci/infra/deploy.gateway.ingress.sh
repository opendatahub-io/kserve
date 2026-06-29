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

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
source "${SCRIPT_DIR}/../common.sh"

cat <<EOF | oc apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: openshift-default
spec:
  controllerName: "openshift.io/gateway-controller/v1"
EOF
  wait_for_pod_ready "openshift-ingress" "app=istiod" 

echo "⏳ Creating a Gateway"
INGRESS_NS=openshift-ingress
oc create namespace ${INGRESS_NS} || true

echo "⏳ Creating gateway memory ConfigMap for parametersRef"
oc apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: gateway-proxy-config
  namespace: ${INGRESS_NS}
data:
  deployment: |
    spec:
      template:
        spec:
          containers:
          - name: istio-proxy
            resources:
              limits:
                memory: 2Gi
              requests:
                memory: 256Mi
EOF

oc apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: openshift-ai-inference
  namespace: ${INGRESS_NS}
spec:
  gatewayClassName: openshift-default
  listeners:
   - name: http
     port: 80
     protocol: HTTP
     allowedRoutes:
       namespaces:
         from: All
  infrastructure:
    parametersRef:
      group: ""
      kind: ConfigMap
      name: gateway-proxy-config
    labels:
      serving.kserve.io/gateway: kserve-ingress-gateway
EOF

wait_for_pod_ready "openshift-ingress" "serving.kserve.io/gateway=kserve-ingress-gateway"
