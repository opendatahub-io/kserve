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

# Image availability check for the images the kserve-module operator renders.
#
# The image params in config/overlays/odh/params.env (rewritten at deploy time
# from RELATED_IMAGE_* by the platform operator) are dead references until
# something deploys them. Two real escapes:
#   RHOAIENG-88996 - RELATED_IMAGE entries for a vLLM CPU Power/Z image that
#                    was never published; every deploy pulled a nonexistent tag
#   RHOAIENG-69531 - llm-d scheduler tag odh-v3.5-EA2 did not exist, all
#                    LLMISVC tests failed with ImagePullBackOff
# A registry lookup at review time catches both before merge.
#
# Anonymous lookups only:
#   - quay.io/opendatahub/* is public and checked
#   - registry.redhat.io / registry.access.redhat.com need credentials even
#     for pulls, so they are skipped with a notice rather than false-failing
#
# Usage:
#   hack/konflux-sim/related_image_availability.sh [params.env ...]
#
# Defaults to the kserve overlay params. Extra files make fault injection a
# one-liner: copy params.env, point one entry at a nonexistent repo, re-run.

set -euo pipefail

CONTAINER_TOOL=${CONTAINER_TOOL:-docker}
command -v "$CONTAINER_TOOL" >/dev/null || { echo "❌ $CONTAINER_TOOL not on PATH" >&2; exit 1; }

files=("$@")
if [ ${#files[@]} -eq 0 ]; then
    files=(config/overlays/odh/params.env config/overlays/odh-modelcache/params.env)
fi

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

# Image-valued params only: values that contain a registry path and a tag or
# digest. Skips the *-upstream-version entries (plain version strings) and any
# non-image config.
cat "${files[@]}" | grep -vE '^\s*#|^\s*$' \
    | grep -E '^[a-zA-Z0-9_-]+=[^ ]*/[^ ]*(:[^ ]+|@sha256:[a-f0-9]{12,})$' \
    | grep -v -- '-upstream-version=' | sort -u > "$workdir/images.txt" || true

if [ ! -s "$workdir/images.txt" ]; then
    echo "❌ no image params found in ${files[*]} - did the params.env format change?"
    exit 1
fi

fail=0
checked=0
skipped=0
while IFS='=' read -r name image; do
    case "$image" in
        registry.redhat.io/*|registry.access.redhat.io/*)
            echo "   skip $name: $image (registry needs auth for anonymous lookup)"
            skipped=$((skipped + 1))
            ;;
        *)
            checked=$((checked + 1))
            if ! "$CONTAINER_TOOL" manifest inspect "$image" >/dev/null 2>&1; then
                echo "❌ $name: $image does not exist in the registry"
                echo "   (RHOAIENG-88996 / RHOAIENG-69531 shape: render references an unpublished image)"
                fail=1
            else
                echo "   ok   $name: $image"
            fi
            ;;
    esac
done < "$workdir/images.txt"

echo "   checked $checked image(s) anonymously, skipped $skipped auth-gated"
if [ "$fail" -ne 0 ]; then
    echo "FAIL: some rendered images do not exist"
    exit 1
fi
echo "PASS: all anonymously checkable rendered images exist in their registry"
