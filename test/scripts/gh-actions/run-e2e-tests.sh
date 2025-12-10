#!/bin/bash

# Copyright 2022 The KServe Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# The script is used to deploy knative and kserve, and run e2e tests.
# Usage: run-e2e-tests.sh $MARKER $PARALLELISM $NETWORK_LAYER

set -o errexit
set -o nounset
set -o pipefail

echo "Starting E2E functional tests ..."
MARKER="${1}"
PARALLELISM="${2:-1}"
NETWORK_LAYER="${3:-'istio'}"
issuer=$(oc get authentication cluster -o jsonpath='{.spec.serviceAccountIssuer}')
if [ -n "$issuer" ]; then
  export TOKEN_AUDIENCES="${TOKEN_AUDIENCES:-$issuer}"
else
  export TOKEN_AUDIENCES="${TOKEN_AUDIENCES:-https://kubernetes.default.svc}"
fi

: "${SKIP_DELETION_ON_FAILURE:=true}"
export SKIP_DELETION_ON_FAILURE

echo "Parallelism requested for pytest is ${PARALLELISM}"

source python/kserve/.venv/bin/activate

pushd test/e2e >/dev/null
  if [[ $MARKER == "raw" && $NETWORK_LAYER == "istio-ingress" ]]; then
    echo "Skipping explainer tests for raw deployment with ingress"
    pytest --capture=tee-sys -m "$MARKER" --ignore=qpext --log-cli-level=INFO -n $PARALLELISM --dist worksteal --network-layer $NETWORK_LAYER --ignore=explainer/
  else
    rc=0
    pytest --capture=tee-sys -m "$MARKER" --ignore=qpext --log-cli-level=INFO -n $PARALLELISM --dist worksteal --network-layer $NETWORK_LAYER || rc=$?
    if [ $rc -ne 0 ]; then
      oc get authpolicies -A -oyaml || true
      oc get llmisvc -A -oyaml || true
      exit $rc
    fi
  fi
popd
