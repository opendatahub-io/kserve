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

# Install Jaeger All-in-One via Helm for LLMISVC tracing e2e on OpenShift CI.
# Does NOT use the OpenShift Distributed Tracing / Tempo OLM operators.
#
# Wraps hack/setup/infra/manage.jaeger-helm.sh with:
#   - Helm CLI install into the repo bin/
#   - OpenShift-safe values via JAEGER_VALUES_FILE (quoted -f)
#   - Service port verification for OTLP (:4317) and Query (:16686)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
JAEGER_NAMESPACE="${JAEGER_NAMESPACE:-observability}"
JAEGER_RELEASE_NAME="${JAEGER_RELEASE_NAME:-jaeger}"
VALUES_FILE="${SCRIPT_DIR}/jaeger/values-openshift.yaml"

echo "⏳ Installing Jaeger All-in-One (Helm) into namespace ${JAEGER_NAMESPACE}"

# Helm + kubectl for manage.jaeger-helm.sh (CI images often ship only oc).
"${PROJECT_ROOT}/hack/setup/cli/install-helm.sh"
export PATH="${PROJECT_ROOT}/bin:${PATH}"
if ! command -v kubectl >/dev/null 2>&1; then
  OC_PATH=$(command -v oc || true)
  if [ -n "$OC_PATH" ]; then
    ln -sf "$OC_PATH" "${PROJECT_ROOT}/bin/kubectl"
  else
    echo "ERROR: oc binary not found in PATH" >&2
    exit 1
  fi
fi

export JAEGER_NAMESPACE JAEGER_RELEASE_NAME
# Prefer JAEGER_VALUES_FILE (quoted -f) over stuffing paths into JAEGER_EXTRA_ARGS.
export JAEGER_VALUES_FILE="${VALUES_FILE}"

"${PROJECT_ROOT}/hack/setup/infra/manage.jaeger-helm.sh"

echo "⏳ Verifying Jaeger Service ports (OTLP 4317, Query 16686)..."
ports="$(oc get svc -n "${JAEGER_NAMESPACE}" \
  -o jsonpath='{range .spec.ports[*]}{.port}{"\n"}{end}' -- "${JAEGER_RELEASE_NAME}")"
for required in 4317 16686; do
  if ! grep -qx "${required}" <<<"${ports}"; then
    echo "ERROR: Service ${JAEGER_RELEASE_NAME} in ${JAEGER_NAMESPACE} missing port ${required}"
    oc get svc -n "${JAEGER_NAMESPACE}" -o yaml -- "${JAEGER_RELEASE_NAME}" || true
    exit 1
  fi
done

echo "✅ Jaeger (Helm) ready — OTLP http://${JAEGER_RELEASE_NAME}.${JAEGER_NAMESPACE}.svc.cluster.local:4317"
