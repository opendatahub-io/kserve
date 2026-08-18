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

# This script tears down the CI namespace for E2E testing.
# It deletes the namespace, and Kubernetes will automatically clean up all resources within it.
set -o errexit
set -o nounset
set -o pipefail

# First positional arg is kept for backward compatibility but no longer used.
# Namespace to tear down (default: kserve-ci-e2e-test)
NAMESPACE="${2:-kserve-ci-e2e-test}"

echo "Tearing down CI namespace: $NAMESPACE"

if ! oc get namespace "$NAMESPACE" >/dev/null 2>&1; then
  echo "Namespace $NAMESPACE does not exist, skipping deletion"
  echo "CI namespace teardown complete"
  exit 0
fi

echo "Deleting namespace $NAMESPACE..."
oc delete namespace "$NAMESPACE" --ignore-not-found --timeout=60s || true

# If the namespace is still around after the initial timeout, resources with
# unprocessed finalizers are blocking deletion (e.g. the controller that would
# remove them was already torn down). Strip finalizers so the namespace can
# drain.
if oc get namespace "$NAMESPACE" >/dev/null 2>&1; then
  echo "Namespace still terminating -- stripping finalizers from stuck resources..."
  for resource in inferenceservices.serving.kserve.io inferencegraphs.serving.kserve.io; do
    for obj in $(oc get "$resource" -n "$NAMESPACE" -o name 2>/dev/null); do
      oc patch "$obj" -n "$NAMESPACE" --type=merge \
        -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
    done
  done
  oc wait --for=delete "namespace/$NAMESPACE" --timeout=60s 2>/dev/null || \
    echo "WARNING: namespace $NAMESPACE did not terminate within timeout"
fi

if oc get namespace "$NAMESPACE" >/dev/null 2>&1; then
  echo "WARNING: namespace $NAMESPACE still exists"
else
  echo "Namespace $NAMESPACE has been deleted"
fi

echo "CI namespace teardown complete"

