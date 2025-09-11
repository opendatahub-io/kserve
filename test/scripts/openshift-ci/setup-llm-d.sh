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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/common.sh"
source "$SCRIPT_DIR/version.sh"
PROJECT_ROOT="$(find_project_root "$SCRIPT_DIR")"

WITH_KSERVE="true"
WITH_KUADRANT="false"

show_usage() {
  cat <<EOF
Sets up llm-d pre-requisites on OpenShift:
- Cert Manager
- Leader Worker Set
- Gateway API configuration for Cluster Ingress Operator
- KServe 
- Kuadrant/Red Hat Connectivity Link (optional)

Usage: $0 [OPTIONS]

Options:
  --with-kserve[=true|false]     Enable/disable KServe installation (default: ✅ true)
  --with-kuadrant[=true|false]   Enable/disable Kuadrant installation (default: ❌ false)
  -h, --help                     Show this help message
EOF
}

parse_bool() {
  case "$1" in
    true|false) printf %s "$1" ;;
    *) echo "Error: expected true|false, got '$1'" >&2; exit 1 ;;
  esac
}

for arg in "$@"; do
  case "$arg" in
    --with-kserve)             WITH_KSERVE=true ;;
    --with-kserve=*)           WITH_KSERVE="$(parse_bool "${arg#*=}")" ;;
    --with-kuadrant)           WITH_KUADRANT=true ;;
    --with-kuadrant=*)         WITH_KUADRANT="$(parse_bool "${arg#*=}")" ;;
    -h|--help)                 show_usage; exit 0 ;;
    *)                         echo "Error: Unknown option '$arg'"; show_usage; exit 1 ;;
  esac
done

echo "🔧 Configuration:"
echo "  KServe deployment: $([ "$WITH_KSERVE" == "true" ] && echo "✅ enabled" || echo "❌ disabled")"
echo "  Kuadrant deployment: $([ "$WITH_KUADRANT" == "true" ] && echo "✅ enabled" || echo "❌ disabled")"
echo ""

server_version=$(get_openshift_server_version)
min_version="4.19.9"
if is_version_newer "${server_version}" "${min_version}"; then
  echo "✅ OpenShift version (${server_version}) is newer than ${min_version} - installing components..."
else
  echo "❌ OpenShift version (${server_version}) older than ${min_version}. This is not supported environment."
  exit 1
fi


$SCRIPT_DIR/infra/deploy.cert-manager.sh
$SCRIPT_DIR/infra/deploy.lws.sh

if [ "${WITH_KSERVE}" == "true" ]; then
  kubectl create ns opendatahub || true

  kubectl kustomize config/crd/ | kubectl apply --server-side=true -f -
  wait_for_crd  llminferenceserviceconfigs.serving.kserve.io  90s

  kustomize build config/overlays/odh | kubectl apply  --server-side=true --force-conflicts -f -
  wait_for_pod_ready "opendatahub" "control-plane=kserve-controller-manager" 300s
fi

$SCRIPT_DIR/infra/deploy.gateway.ingress.sh

if [ "${WITH_KUADRANT}" == "true" ]; then
  $SCRIPT_DIR/infra/deploy.kuadrant.sh
fi
