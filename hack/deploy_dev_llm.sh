#!/bin/bash
set -e

./test/scripts/gh-actions/setup-deps.sh raw istio-gatewayapi-ext false true

# Check if ko command is available, install if not
if ! command -v ko &>/dev/null; then
    echo "Installing ko..."
    go install github.com/google/ko@latest
fi

echo "Deploying KServe with RawDeployment..."
make deploy-dev

echo "Deploy completed successfully!" 