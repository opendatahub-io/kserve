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

set -o errexit
set -o nounset
set -o pipefail

echo "Starting E2E functional tests ..."
if [ $# -eq 2 ]; then
  echo "Parallelism requested for pytest is $2"
else
  echo "No parallelism requested for pytest. Will use default value of 1"
fi

PARALLELISM="${2:-1}"
MY_PATH=$(dirname "$0")
PROJECT_ROOT=$MY_PATH/../../../
echo $PROJECT_ROOT
source python/kserve/.venv/bin/activate
pushd test/e2e >/dev/null
  pytest --collect-only
  pytest --trace-config
  pytest $PROJECT_ROOT/test/e2e -vvv -s --full-trace --setup-show -m "$1" --ignore=qpext --log-cli-level=DEBUG -n $PARALLELISM --dist no
  sleep 30m
popd
echo "Done!"
