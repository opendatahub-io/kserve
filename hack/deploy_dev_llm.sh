#!/bin/bash
set -e

./test/scripts/gh-actions/setup-deps.sh raw istio-gatewayapi-ext false true

# Check if ko command is available, install if not
if ! command -v ko &>/dev/null; then
    echo "Installing ko..."
    go install github.com/google/ko@latest
fi

echo "Creating Istio Gateway ..."
kubectl create ns kserve || true
# Replace gatewayclass name
kubectl apply -f - <<EOF
$(sed 's/envoy/istio/g' config/overlays/test/gateway/ingress_gateway.yaml)
EOF
sleep 10
echo "Waiting for istio gateway to be ready ..."
kubectl wait --timeout=5m -n kserve pod -l serving.kserve.io/gateway=kserve-ingress-gateway --for=condition=Ready

echo "Deploying KServe with RawDeployment..."
make deploy-dev

echo "Deploy completed successfully!" 