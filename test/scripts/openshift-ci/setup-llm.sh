#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/common.sh"
PROJECT_ROOT="$(find_project_root "$SCRIPT_DIR")"

# Get OpenShift server version and execute appropriate script
server_version=$(get_openshift_server_version)
echo "Checking OpenShift server version...($server_version)"

# Execute script based on version comparison
if version_compare "$server_version" "4.19.9"; then
  echo "🎯 Server version ($server_version) is 4.19.9 or higher - continue with the script"
else
  echo "🎯 Server version ($server_version) is not supported so stop the script"
  exit 1
fi

# Installing Cert Manager
$SCRIPT_DIR/infra/deploy.cert-manager.sh

# Installing LWS Operator" 
$SCRIPT_DIR/infra/deploy.lws.sh

# Installing KServe
kubectl create ns opendatahub || true

kubectl kustomize config/crd/ | kubectl apply --server-side=true -f -
wait_for_crd  llminferenceserviceconfigs.serving.kserve.io  90s

kustomize build config/overlays/odh | kubectl apply  --server-side=true --force-conflicts -f -
wait_for_pod_ready "opendatahub" "control-plane=kserve-controller-manager" 300s

# Installing Gateway Ingress 
$SCRIPT_DIR/infra/deploy.gateway.ingress.sh

# Installing RHCL(Kuadrant) operator
$SCRIPT_DIR/infra/deploy.kudrant.sh
