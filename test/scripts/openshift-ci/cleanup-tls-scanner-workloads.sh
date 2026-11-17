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

# Removes only the TLS scanner fixture resources from their explicitly marked
# namespace. The namespace itself is retained for safe, repeatable local use.
set -o errexit
set -o nounset
set -o pipefail

: "${TLS_SCANNER_NAMESPACE:=kserve-tls-scanner}"
readonly NAMESPACE_MARKER_KEY="tests.kserve.io/managed-by"
readonly NAMESPACE_MARKER_VALUE="kserve-tls-scanner"
readonly FIXTURE_LABEL="tests.kserve.io/tls-scanner-fixture=true"
readonly RUNTIME_LABEL="tests.kserve.io/tls-scanner-runtime=true"

command -v oc >/dev/null 2>&1 || {
  echo "The oc CLI is required to clean up TLS scanner workloads." >&2
  exit 1
}

if ! oc get namespace "$TLS_SCANNER_NAMESPACE" >/dev/null 2>&1; then
  echo "TLS scanner namespace '$TLS_SCANNER_NAMESPACE' does not exist; nothing to clean up."
  exit 0
fi

namespace_marker="$(oc get namespace "$TLS_SCANNER_NAMESPACE" \
  -o go-template="{{ index .metadata.labels \"$NAMESPACE_MARKER_KEY\" }}")"
if [[ "$namespace_marker" != "$NAMESPACE_MARKER_VALUE" ]]; then
  echo "Refusing cleanup in namespace '$TLS_SCANNER_NAMESPACE': expected label $NAMESPACE_MARKER_KEY=$NAMESPACE_MARKER_VALUE." >&2
  exit 1
fi

oc delete inferenceservices.serving.kserve.io -n "$TLS_SCANNER_NAMESPACE" \
  -l "$FIXTURE_LABEL" --ignore-not-found --wait=true --timeout=5m
oc delete llminferenceservices.serving.kserve.io -n "$TLS_SCANNER_NAMESPACE" \
  -l "$FIXTURE_LABEL" --ignore-not-found --wait=true --timeout=5m
oc delete llminferenceserviceconfigs.serving.kserve.io -n "$TLS_SCANNER_NAMESPACE" \
  -l "$FIXTURE_LABEL" --ignore-not-found --wait=true --timeout=2m
oc delete servingruntimes.serving.kserve.io -n "$TLS_SCANNER_NAMESPACE" \
  -l "$RUNTIME_LABEL" --ignore-not-found --wait=true --timeout=2m

echo "Removed TLS scanner fixtures from $TLS_SCANNER_NAMESPACE; retained the marked namespace."
