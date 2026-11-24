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

# Manifest path contract for the kserve-module operator image.
#
# The module operator ships kserve manifests inside its image and copies them
# to a writable directory at startup. Four places must agree; nothing else
# compares them, and a mismatch leaves the operator crash-looping or applying
# the wrong directory (the model-registry RHOAIENG-99568 shape: image shipped
# /opt/manifests/modelregistry/... while the operator read /opt/manifests/aihub/...).
#
#   1. kserve-module-controller.Dockerfile  COPY ... /opt/manifests-template/
#   2. kserve-module/cmd/kserve-module/main.go      const manifestsTemplatePath
#   3. kserve-module/pkg/kservemodule/reconciler.go workDir (runtime copy target)
#   4. kserve-module/config/manager.yaml            volume mount of workDir
#
# Greps source locations rather than duplicating the paths here, so editing
# any one of the four without the others fails this check with the exact
# locations named.

set -euo pipefail

repo=$(git rev-parse --show-toplevel)
cd "$repo"

dockerfile="kserve-module-controller.Dockerfile"
main_go="kserve-module/cmd/kserve-module/main.go"
reconciler_go="kserve-module/pkg/kservemodule/reconciler.go"
manager_yaml="kserve-module/config/manager.yaml"

fail=0

die() {
    echo "❌ $*"
    fail=1
}

# Escape ERE metacharacters so extracted paths compare literally: with a
# bare interpolation, workDir '/opt/manifests.v2' would regex-match
# mountPath '/opt/manifests-v2' ('.' is a wildcard) and the contract
# would pass on different paths.
re_escape() {
    printf '%s' "$1" | sed -e 's/[][\.^$*+?(){}|]/\\&/g'
}

# 1. Dockerfile: the manifests directory produced by get_kserve_manifests.sh
#    is copied into the image at the template path (last token of a COPY line).
template_dockerfile=$(grep -oE '^COPY[^#]* /opt/manifests-template/$' "$dockerfile" \
    | grep -oE '/opt/manifests-template/$' | sed 's:/$::' || true)
if [ -z "$template_dockerfile" ]; then
    die "$dockerfile does not COPY the manifests into /opt/manifests-template/"
fi

# 2. main.go: refuses to start unless the template path exists.
template_main=$(grep -oE 'manifestsTemplatePath = "[^"]+"' "$main_go" | grep -oE '/[^"]+' || true)
if [ -z "$template_main" ]; then
    die "$main_go no longer defines const manifestsTemplatePath"
fi

# 3. reconciler: copies the template tree into a writable work dir at startup.
workdir_reconciler=$(grep -oE 'workDir := "[^"]+"' "$reconciler_go" | grep -oE '/[^"]+' || true)
if [ -z "$workdir_reconciler" ]; then
    die "$reconciler_go no longer sets the runtime workDir constant"
fi

# 4. manager.yaml: the work dir must be a mounted volume - the container runs
#    with readOnlyRootFilesystem, so an unmounted work dir makes the startup
#    copy fail on every reconcile.
workdir_re=$(re_escape "$workdir_reconciler")
mount=$(grep -oE "mountPath: ${workdir_re}\$" "$manager_yaml" || true)
if [ -z "$mount" ]; then
    die "$manager_yaml does not mount $workdir_reconciler (readOnlyRootFilesystem would break the startup copy)"
fi
volume_name=$(grep -B1 "mountPath: ${workdir_re}\$" "$manager_yaml" | grep -oE 'name: [a-z-]+' | head -1 | cut -d' ' -f2)
if [ -z "$volume_name" ] || ! grep -qE "^      - name: ${volume_name}$" "$manager_yaml"; then
    die "$manager_yaml mounts $workdir_reconciler as '$volume_name' but defines no such volume"
fi

if [ "$template_dockerfile" != "$template_main" ]; then
    die "Dockerfile copies to $template_dockerfile but main.go reads $template_main"
fi

echo "   Dockerfile COPY destination : $template_dockerfile"
echo "   main.go manifestsTemplatePath: $template_main"
echo "   reconciler workDir          : $workdir_reconciler"
echo "   manager.yaml mount          : $mount (volume: $volume_name)"

if [ "$fail" -ne 0 ]; then
    echo "FAIL: manifest path contract broken"
    exit 1
fi
echo "PASS: image manifest path, operator constants and Deployment mount agree"
