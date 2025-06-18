#!/bin/bash
set -e

# Cleanup function - restore files using git
cleanup() {
    echo "Restoring files using git..."
    git restore ./config/configmap/inferenceservice.yaml || true
    git restore ./config/default/manager_image_patch.yaml || true
    echo "Files restored to original state"
}

# Set signal traps (INT=Ctrl+C, TERM=kill, EXIT=script termination)
trap cleanup INT TERM EXIT

# Check and install Gateway API CRDs if needed
echo "Checking Gateway API CRDs..."
if ! kubectl get crd gatewayclasses.gateway.networking.k8s.io >/dev/null 2>&1 || ! kubectl get crd httproutes.gateway.networking.k8s.io >/dev/null 2>&1; then
    echo "Installing Gateway API + Extensions CRDs..."
    ./test/scripts/gh-actions/setup-deps.sh raw istio-gatewayapi-ext
else
    echo "Gateway API + Extensions CRDs already installed, skipping..."
fi

echo "Modifying configuration files..."
sed 's/Serverless/RawDeployment/g' -i ./config/configmap/inferenceservice.yaml

if [[ ! -z "${KSERVE_IMG}" ]]; then
    echo "Using custom KSERVE_IMG: ${KSERVE_IMG}"
    sed -i "s|kserve/kserve-controller:latest|${KSERVE_IMG}|g" ./config/default/manager_image_patch.yaml
fi

echo "Deploying KServe with RawDeployment..."
make deploy

echo "Deploy completed successfully!" 