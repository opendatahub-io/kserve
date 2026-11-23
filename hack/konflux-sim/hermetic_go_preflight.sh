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

# Hermetic Go build preflight for the Konflux-built images.
#
# The downstream (red-hat-data-services) Konflux builds run with network
# egress disabled and every Go module served from the cachi2 prefetch cache
# (RHOAIENG-89178 is a build that failed exactly there). The midstream
# .tekton PR pipelines are not hermetic, so a go.sum that is missing an
# entry, or a module the prefetch config does not cover, only surfaces days
# later in the downstream build.
#
# Two phases, mirroring what the Dockerfiles do:
#   1. network phase - populate an isolated GOMODCACHE (go mod download)
#   2. offline phase - build every Konflux binary target with GOPROXY=off,
#      so a module that was never prefetched fails here instead
#
# Targets and tags mirror the .tekton/*pull*.yaml PipelineRuns and their
# Dockerfiles: same package, same CGO_ENABLED/GOOS, same -tags. Building
# ./... instead would not prove the shipped binaries link.
#
# Usage:
#   hack/konflux-sim/hermetic_go_preflight.sh <module-dir> <cmd|binary|tags>...
#
# where each spec is <cmd-dir>|<output-binary>|<build-tags>, e.g.:
#   hack/konflux-sim/hermetic_go_preflight.sh . cmd/manager|manager|distro
#   hack/konflux-sim/hermetic_go_preflight.sh kserve-module cmd/kserve-module|module-manager|
#
# The empty third field means "no tags" (the module operator builds without
# GOTAGS; its PipelineRun passes no build-args).

set -euo pipefail

module_dir=${1:?usage: hermetic_go_preflight.sh <module-dir> <cmd|binary|tags>...}
shift
if [ $# -eq 0 ]; then
    echo "no build targets given" >&2
    exit 1
fi

workdir=$(mktemp -d)
# Go marks module cache files read-only; chmod before rm or the cleanup fails.
trap 'chmod -R u+w "$workdir" 2>/dev/null || true; rm -rf "$workdir"' EXIT
modcache="$workdir/gomodcache"

cd "$module_dir"

# GOTOOLCHAIN=local: the Konflux builder image ships one Go and cannot
# download another. A go.mod bump past what the builder has must fail loudly
# here, not silently fetch a toolchain mid-build.
export GOTOOLCHAIN=local

echo "== network phase: populating isolated module cache"
GOMODCACHE="$modcache" GOFLAGS=-mod=readonly go mod download
GOMODCACHE="$modcache" go mod verify

for spec in "$@"; do
    IFS='|' read -r cmd binary tags <<<"$spec"
    echo "== offline phase: building $cmd -> $binary (tags: '${tags:-none}')"
    GOMODCACHE="$modcache" GOPROXY=off GONOPROXY=none GOFLAGS=-mod=readonly \
        CGO_ENABLED=0 GOOS=linux \
        go build -buildvcs=false -tags "$tags" -o "$workdir/$binary" "./$cmd"
    ls -lh "$workdir/$binary"
done

echo "PASS: all targets built with GOPROXY=off from the prefetched cache"
