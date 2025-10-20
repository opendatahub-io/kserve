#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Configuring SSL for Authorino..."

# Add ServingCert annotation to Authorino service for generating the cert
oc annotate svc/authorino-authorino-authorization service.beta.openshift.io/serving-cert-secret-name=authorino-server-cert -n kuadrant-system --overwrite

# Wait for secret creation
echo "Waiting for SSL certificate secret..."
timeout=60
while ! oc get secret authorino-server-cert -n kuadrant-system &>/dev/null && [ $timeout -gt 0 ]; do
    sleep 2
    ((timeout-=2))
done

if [ $timeout -le 0 ]; then
    echo "ERROR: Timeout waiting for secret creation"
    exit 1
fi

echo "Secret created, applying Authorino configuration..."
oc apply -f "$SCRIPT_DIR/ssl-authorino.yaml"

echo "SSL Authorino configuration completed!"
