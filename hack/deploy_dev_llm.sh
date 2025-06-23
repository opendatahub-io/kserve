#!/bin/bash
set -e

./test/scripts/gh-actions/setup-deps.sh raw istio-gatewayapi-ext

echo "Deploying KServe with RawDeployment..."
make deploy-dev

echo "Deploy completed successfully!" 