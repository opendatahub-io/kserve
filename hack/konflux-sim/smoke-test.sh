#!/bin/bash

# Copyright 2026 The KServe Contributors
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

# Smoke-test one image built by the Konflux build simulation.
#
# Usage: hack/konflux-sim/smoke-test.sh TYPE IMAGE [ARG]
#
# Konflux proves a pull request by building the image; it never runs the
# binary. The simulation adds the cheapest assertion that the built artifact
# is runnable, because the failure class is real: a Dockerfile whose runtime
# stage drops or renames the binary, or a Python image whose venv is missing
# a package, still builds green and crash-loops on the cluster.
#
# Types (assigned per dockerfile by generate-build-matrix.sh):
#   go-help          the container binary answers --help with exit 0 and
#                    non-empty usage text; an empty --help fails closed
#   python-import    the venv python imports ARG, the package the image ships
#   module-operator  manager --help exits 0 and /opt/manifests-template holds
#                    the component dirs the operator loads at startup
#   builder-only     no runnable image was built (partial simulation); ARG
#                    carries the note explaining what was not simulated

set -euo pipefail

smoke_type="${1:-}"
image="${2:-}"
smoke_arg="${3:-}"

if [[ -z "${smoke_type}" || -z "${image}" ]]; then
  echo "Usage: ${0} TYPE IMAGE [ARG]" >&2
  exit 1
fi

container_tool="${CONTAINER_TOOL:-}"
if [[ -z "${container_tool}" ]]; then
  for candidate in docker podman; do
    if command -v "${candidate}" >/dev/null 2>&1; then
      container_tool="${candidate}"
      break
    fi
  done
fi
if [[ -z "${container_tool}" ]]; then
  echo "ERROR: no container tool found (docker or podman)" >&2
  exit 1
fi

# fail MESSAGE...: report the failing assertion with the image under test.
fail() {
  echo "ERROR: smoke-test '${smoke_type}' failed for image '${image}': ${*}" >&2
  exit 1
}

# run_capture CMD...: run CMD, capture its combined output in RUN_OUTPUT and
# its exit status in RUN_STATUS, without tripping set -e.
run_capture() {
  set +e
  RUN_OUTPUT="$("${@}" 2>&1)"
  RUN_STATUS=$?
  set -e
}

case "${smoke_type}" in
  go-help)
    run_capture "${container_tool}" run --rm "${image}" --help
    if [[ "${RUN_STATUS}" -ne 0 ]]; then
      printf '%s\n' "${RUN_OUTPUT}" >&2
      fail "binary --help exited ${RUN_STATUS}"
    fi
    if [[ -z "${RUN_OUTPUT}" ]]; then
      fail "--help exited 0 but printed no usage text; the runtime image may not contain the expected binary"
    fi
    echo "PASS: ${image}: --help exited 0 with usage text"
    ;;
  python-import)
    if [[ -z "${smoke_arg}" ]]; then
      fail "python-import needs a module name argument"
    fi
    run_capture "${container_tool}" run --rm --entrypoint python "${image}" -c "import ${smoke_arg}"
    if [[ "${RUN_STATUS}" -ne 0 ]]; then
      printf '%s\n' "${RUN_OUTPUT}" >&2
      fail "python could not import ${smoke_arg}"
    fi
    echo "PASS: ${image}: python imported ${smoke_arg}"
    ;;
  module-operator)
    run_capture "${container_tool}" run --rm "${image}" --help
    if [[ "${RUN_STATUS}" -ne 0 ]]; then
      printf '%s\n' "${RUN_OUTPUT}" >&2
      fail "manager --help exited ${RUN_STATUS}"
    fi
    # /opt/manifests-template is the operator's startup contract: main.go
    # refuses to start when it is missing, and loadComponentReleases reads
    # <component>/component_metadata.yaml for kserve and modelcontroller.
    # modelexpress is rendered but carries no component_metadata.yaml.
    for component in kserve modelcontroller modelexpress; do
      run_capture "${container_tool}" run --rm --entrypoint ls "${image}" \
        "/opt/manifests-template/${component}"
      if [[ "${RUN_STATUS}" -ne 0 ]]; then
        printf '%s\n' "${RUN_OUTPUT}" >&2
        fail "bundled manifests: /opt/manifests-template/${component} is missing"
      fi
      if [[ -z "${RUN_OUTPUT}" ]]; then
        fail "bundled manifests: /opt/manifests-template/${component} is empty"
      fi
    done
    for component in kserve modelcontroller; do
      run_capture "${container_tool}" run --rm --entrypoint ls "${image}" \
        "/opt/manifests-template/${component}/component_metadata.yaml"
      if [[ "${RUN_STATUS}" -ne 0 ]]; then
        fail "bundled manifests: ${component}/component_metadata.yaml is missing; the operator cannot load its component releases"
      fi
    done
    echo "PASS: ${image}: manager --help and bundled-manifests contract"
    ;;
  builder-only)
    echo "::warning::no runnable image for ${image}: ${smoke_arg}"
    echo "PASS (partial): ${image}: only the builder stage was simulated. ${smoke_arg}"
    ;;
  *)
    fail "unknown smoke type '${smoke_type}'"
    ;;
esac
