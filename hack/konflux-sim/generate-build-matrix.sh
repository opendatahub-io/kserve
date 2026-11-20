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

# Generate the GitHub Actions matrix of Konflux PR-build targets.
#
# The .tekton/ PipelineRuns are the source of truth for what Konflux builds
# from every pull request: dockerfile, build context, build args and build
# platforms, all handed to the shared odh-konflux-central
# pipeline/multi-arch-container-build.yaml. This script reads them and emits
# one JSON matrix entry per target so the konflux-build-simulation workflow
# builds the same images from the same inputs.
#
# Because the matrix is derived, not hardcoded, it cannot drift: a new
# PipelineRun appears in CI on its next run, and anything this script cannot
# map (an unknown pipeline, a dockerfile without a simulation spec, a
# build-arg it cannot pass safely) fails loudly here instead of being quietly
# skipped. Failing to generate is always an error; this script never emits an
# empty matrix.
#
# Output on stdout:
#   {"include": [ {name, dockerfile, context, build_args_str, target, smoke,
#                   smoke_arg, timeout, manual}, ... ]}
# consumable as
#   matrix: ${{ fromJSON(<this output>) }}
#
# Usage:
#   hack/konflux-sim/generate-build-matrix.sh [--include-manual]
#
#   --include-manual  also emit targets whose PipelineRun is comment-gated
#                     (pipelinesascode.tekton.dev/on-comment, e.g.
#                     odh-kserve-autogluon-server's /build-konflux), mirroring
#                     how Konflux builds them only on request.
#
# Limitations, reported rather than hidden:
# - Only the runner's native platform is built. Other Konflux build-platforms
#   are printed as warnings; the workflow has no emulation configured.
# - odh-kserve-kueue-controller's runtime base image lives in a terms-gated
#   registry.redhat.io repo that needs an authenticated pull, so only its
#   builder stage is simulated.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# The simulation runs on x86_64 GitHub-hosted runners.
NATIVE_PLATFORM="${SIM_NATIVE_PLATFORM:-linux/x86_64}"
# The shared pipeline's build-platforms default when a PipelineRun does not
# override it (odh-konflux-central pipeline/multi-arch-container-build.yaml).
PIPELINE_DEFAULT_PLATFORM="linux/x86_64"
CONTAINER_BUILD_PIPELINE="pipeline/multi-arch-container-build.yaml"

include_manual=false
if [[ "${1:-}" == "--include-manual" ]]; then
  include_manual=true
elif [[ -n "${1:-}" ]]; then
  echo "ERROR: unknown argument '${1}' (expected --include-manual)" >&2
  exit 1
fi

yq_bin="${YQ:-}"
if [[ -z "${yq_bin}" ]]; then
  if [[ -x "${REPO_ROOT}/bin/yq" ]]; then
    yq_bin="${REPO_ROOT}/bin/yq"
  elif command -v yq >/dev/null 2>&1; then
    yq_bin="yq"
  fi
fi
if [[ -z "${yq_bin}" ]]; then
  echo "ERROR: yq not found; run 'make yq' or set YQ=<path to yq>" >&2
  exit 1
fi

# fail MESSAGE...: report a generation error and exit non-zero.
fail() {
  echo "ERROR: ${*}" >&2
  exit 1
}

# param_value FILE NAME: print the value of a scalar spec param, or nothing.
param_value() {
  "${yq_bin}" ".spec.params // [] | .[] | select(.name == \"${2}\") | .value" "${1}"
}

entries="$(mktemp)"
trap 'rm -f "${entries}"' EXIT

found_any=false
shopt -s nullglob
for run_file in "${REPO_ROOT}"/.tekton/*.yaml; do
  file_name="$(basename "${run_file}")"

  # Push PipelineRuns build merged master commits, not pull requests.
  if [[ "${file_name}" == *-push.yaml ]]; then
    continue
  fi

  path_in_repo="$("${yq_bin}" \
    '.spec.pipelineRef.params // [] | .[] | select(.name == "pathInRepo") | .value' \
    "${run_file}")"
  case "${path_in_repo}" in
    "${CONTAINER_BUILD_PIPELINE}")
      ;;
    integration-tests/*)
      # Group-testing pipelines wire already-built images into an integration
      # suite; they build no image this simulation could reproduce.
      continue
      ;;
    *)
      fail "${file_name} resolves pipeline '${path_in_repo}', which this generator does not know how to simulate. Teach it about the new pipeline instead of skipping it."
      ;;
  esac

  dockerfile="$(param_value "${run_file}" dockerfile)"
  if [[ -z "${dockerfile}" ]]; then
    fail "${file_name} sets no dockerfile param."
  fi
  if [[ "${dockerfile}" == *".."* || "${dockerfile}" =~ [[:space:]] ]]; then
    fail "${file_name}: refusing dockerfile path '${dockerfile}' (whitespace or parent traversal)."
  fi

  context="$(param_value "${run_file}" path-context)"
  context="${context:-.}"
  if [[ ! -d "${REPO_ROOT}/${context}" ]]; then
    fail "${file_name}: build context '${context}' does not exist in the repo."
  fi

  # Replicate the Konflux buildah task's dockerfile resolution exactly: it
  # looks inside the build context first and falls back to the source root
  # (task-buildah-remote-oci-ta). kserve-storage-initializer relies on the
  # first branch, odh-kserve-autogluon-server on the second.
  if [[ -f "${REPO_ROOT}/${context}/${dockerfile}" ]]; then
    dockerfile="${context}/${dockerfile}"
  elif [[ ! -f "${REPO_ROOT}/${dockerfile}" ]]; then
    fail "${file_name}: dockerfile '${dockerfile}' not found in build context '${context}' or at the repo root."
  fi
  dockerfile="${dockerfile#./}"

  output_image="$(param_value "${run_file}" output-image)"
  if [[ -z "${output_image}" ]]; then
    fail "${file_name} sets no output-image param."
  fi
  name="$(basename "${output_image%%:*}")"

  # Build args are forwarded verbatim to the container build. Keep them free
  # of whitespace so the workflow can pass them as single words.
  build_args_str=""
  while IFS= read -r build_arg; do
    if [[ -z "${build_arg}" ]]; then
      continue
    fi
    if [[ ! "${build_arg}" =~ ^[A-Za-z_][A-Za-z0-9_]*=[^[:space:]]+$ ]]; then
      fail "${file_name}: build-arg '${build_arg}' is not a whitespace-free NAME=VALUE; extend the workflow if one is ever needed."
    fi
    build_args_str="${build_args_str:+${build_args_str} }${build_arg}"
  done < <("${yq_bin}" \
    '.spec.params // [] | .[] | select(.name == "build-args") | .value // [] | .[]' \
    "${run_file}")

  platforms="$("${yq_bin}" \
    '.spec.params // [] | .[] | select(.name == "build-platforms") | .value // [] | .[]' \
    "${run_file}")"
  if [[ -z "${platforms}" ]]; then
    platforms="${PIPELINE_DEFAULT_PLATFORM}"
  fi
  builds_native=false
  while IFS= read -r platform; do
    if [[ -z "${platform}" ]]; then
      continue
    fi
    if [[ "${platform}" == "${NATIVE_PLATFORM}" ]]; then
      builds_native=true
    else
      echo "::warning file=${file_name}::${name}: Konflux also builds ${platform}; the simulation covers ${NATIVE_PLATFORM} only" >&2
    fi
  done <<< "${platforms}"
  if [[ "${builds_native}" != "true" ]]; then
    fail "${file_name}: none of its build-platforms covers the native ${NATIVE_PLATFORM}; the workflow needs emulation support before it can simulate this target."
  fi

  # Comment-gated targets mirror their Konflux trigger: built only on request
  # (--include-manual / the workflow_dispatch input).
  on_comment="$("${yq_bin}" \
    '.metadata.annotations["pipelinesascode.tekton.dev/on-comment"] // ""' \
    "${run_file}")"
  manual=false
  if [[ -n "${on_comment}" ]]; then
    manual=true
    if [[ "${include_manual}" != "true" ]]; then
      echo "::notice ::${name}: comment-gated Konflux target (${on_comment}); simulated only with --include-manual" >&2
      continue
    fi
  fi

  # Per-dockerfile simulation contract. A dockerfile without an entry here
  # fails generation: a new Konflux target must not ship unvalidated.
  case "${dockerfile}" in
    Dockerfile|agent.Dockerfile|router.Dockerfile|llmisvc-controller.Dockerfile|localmodel.Dockerfile|localmodel-agent.Dockerfile)
      smoke="go-help"
      smoke_arg=""
      target=""
      timeout=60
      ;;
    kserve-module-controller.Dockerfile)
      smoke="module-operator"
      smoke_arg=""
      target=""
      timeout=60
      ;;
    python/storage-initializer.Dockerfile)
      smoke="python-import"
      smoke_arg="kserve_storage"
      target=""
      timeout=90
      ;;
    python/autogluon.Dockerfile)
      smoke="python-import"
      smoke_arg="autogluon"
      target=""
      timeout=150
      ;;
    kserve-kueue-controller.Dockerfile)
      smoke="builder-only"
      smoke_arg="runtime base registry.redhat.io/ubi9/ubi-minimal-pqc is terms-gated and needs an authenticated pull, which GitHub-hosted runners do not have; only the builder stage is simulated"
      target="builder"
      timeout=30
      ;;
    *)
      fail "no simulation spec for dockerfile '${dockerfile}' (${file_name}). Add one to the case statement in ${0} so the new Konflux target is validated."
      ;;
  esac

  jq -cn \
    --arg name "${name}" \
    --arg dockerfile "${dockerfile}" \
    --arg context "${context}" \
    --arg build_args_str "${build_args_str}" \
    --arg target "${target}" \
    --arg smoke "${smoke}" \
    --arg smoke_arg "${smoke_arg}" \
    --argjson timeout "${timeout}" \
    --argjson manual "${manual}" \
    '{name: $name, dockerfile: $dockerfile, context: $context,
      build_args_str: $build_args_str, target: $target, smoke: $smoke,
      smoke_arg: $smoke_arg, timeout: $timeout, manual: $manual}' \
    >> "${entries}"
  found_any=true
done

if [[ "${found_any}" != "true" ]]; then
  fail "no Konflux PR-build PipelineRuns found under .tekton/."
fi

# Sort by target name so the matrix, and its noise in CI logs, is stable.
sort -o "${entries}" "${entries}"
jq -s '{include: .}' "${entries}"
