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

# Hermetic Python preflight for the uv-based Konflux images
# (kserve-storage-initializer, odh-kserve-autogluon-server).
#
# Downstream Konflux builds install these images' dependencies from the
# cachi2 prefetch cache with the network off (RHOAIENG-88284/88285/88286 and
# RHOAIENG-87548/87549 are prefetch-input drift that only surfaced in the
# downstream Conforma image check). The midstream .tekton PR pipelines are
# not hermetic, so a uv.lock that cannot be satisfied offline only surfaces
# downstream.
#
# Two phases per project, mirroring the Dockerfiles:
#   1. network phase - uv sync (plus the unlocked `uv pip install` specs the
#      storage-initializer Dockerfile runs) into an ISOLATED UV_CACHE_DIR
#   2. offline phase - delete the venv, recreate it, and install again with
#      --offline; anything missing from the cache fails here
#
# The venv is deleted between phases on purpose: if it survived, the offline
# pass could satisfy itself from installed packages and prove nothing.
#
# Usage:
#   hack/konflux-sim/hermetic_python_preflight.sh <project-dir> [pip-spec...]
#
#     project-dir  directory with pyproject.toml + uv.lock
#     pip-spec     extra specs the Dockerfile installs outside the lock
#                  (storage-initializer: krbcontext==0.10 hdfs~=2.6.0
#                   requests-kerberos==0.14.0 tomli)
#
# Environment:
#   UV             uv binary (default: uv on PATH). Use the version pinned
#                  in kserve-deps.env (UV_VERSION) - a different uv may
#                  resolve a different graph than the one that ships.
#   UV_CACHE_DIR   cache dir (default: throwaway under a temp dir). Set it
#                  to a stable path to reuse the warm cache across runs.
#   UV_PYTHON      interpreter for uv venv (default: python3). The Dockerfile
#                  images pin their own (3.11 / 3.12); pass a matching one.
#   UV_SYNC_ARGS   extra args for uv sync (e.g. "--extra confidential").
#
# The offline phase only proves the install where the target platform can be
# installed: on macOS the cache is keyed for darwin, so only the warm phase
# runs there and the script says so instead of reporting a pass that proves
# nothing.

set -euo pipefail

project_dir=${1:?usage: hermetic_python_preflight.sh <project-dir> [pip-spec...]}
shift || true
uv_bin=${UV:-uv}
python=${UV_PYTHON:-python3}

# Split UV_SYNC_ARGS once into an array so each option stays one argument
# and pathname expansion cannot rewrite its value mid-word (the
# 'config[1].toml' CWE-88 shape). The length guard keeps the empty-array
# expansion safe under set -u on bash 3.2 (macOS /bin/bash).
read -r -a sync_args <<<"${UV_SYNC_ARGS:-}"

sync_with_args() {
    # shellcheck disable=SC2034  # VIRTUAL_ENV is consumed by uv
    if [ "${#sync_args[@]}" -gt 0 ]; then
        VIRTUAL_ENV="$venv" "$uv_bin" sync --active "$@" "${sync_args[@]}" --quiet
    else
        VIRTUAL_ENV="$venv" "$uv_bin" sync --active "$@" --quiet
    fi
}

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
UV_CACHE_DIR=${UV_CACHE_DIR:-$workdir/uv-cache}
export UV_CACHE_DIR

venv="$workdir/venv"
cd "$project_dir"

echo "== network phase: warming uv cache at $UV_CACHE_DIR"
"$uv_bin" venv "$venv" --python "$python" --quiet
# --locked, not --frozen, in the warm phase: the Dockerfiles run a plain
# `uv sync`, which re-resolves when pyproject and uv.lock have drifted.
# --frozen would install the stale lock happily and pass, hiding exactly
# the lock/metadata drift (RHOAIENG-47258/52913 shape) this preflight
# exists to catch. The offline phase stays --frozen: it only proves the
# warmed cache can rebuild the venv.
sync_with_args --locked
if [ $# -gt 0 ]; then
    echo "   plus unlocked pip specs: $*"
    VIRTUAL_ENV="$venv" "$uv_bin" pip install --quiet "$@"
fi

if [ "$(uname -s)" != "Linux" ]; then
    echo "SKIP: not Linux - only the warm phase ran on $(uname -s), no offline proof"
    exit 0
fi

echo "== offline phase: rebuilding the venv from the warmed cache only"
rm -rf "$venv"
"$uv_bin" venv "$venv" --python "$python" --quiet
sync_with_args --frozen --offline
if [ $# -gt 0 ]; then
    VIRTUAL_ENV="$venv" "$uv_bin" pip install --offline --quiet "$@"
fi

echo "PASS: $project_dir installs with --offline from the warmed cache"
