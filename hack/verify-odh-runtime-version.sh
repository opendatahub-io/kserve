#!/bin/bash
# Verify that the opendatahub.io/runtime-version annotation is stamped on every
# accelerator LLMInferenceServiceConfig rendered by the odh overlay.
#
# A missing params.env key already fails `kustomize build` loudly (the
# replacement source breaks). What nothing else catches is the silent
# regression class: a key that is present but empty, or a replacement block
# that is dropped or mistargeted. Both ship `opendatahub.io/runtime-version: ""`
# on rendered configs without failing anything. This check closes that gap.
#
# Each rendered accelerator config must carry a non-empty annotation matching
# its own dedicated *-upstream-version key in params.env: base presets match
# the base keys, -fast-N variants match the -fast-N keys. This covers the
# nvidia topology-variant fan-out (4-way) and the spyre arch fan-out (3-way)
# in both base and fast forms. Expected values are parsed from params.env, not
# hardcoded, so legitimate version bumps do not break this check; it asserts
# wiring, not pinned values.
#
# Also verified here: every accelerator config must carry a non-empty
# opendatahub.io/config-base-name annotation equal to its own rendered
# metadata.name (RHOAIENG-78121). The operators prefix metadata.name with the
# platform version at reconcile time while annotations pass through untouched,
# so this annotation is the version-stable identity the Dashboard matches on.
# In kustomize output the name is still unprefixed, so the assertion is exact
# equality, which also pins the fast-variant semantic: a -fast-N config must
# carry its own -fast-N name, not its base template's.
#
# The odh-xks and odh-test overlays include the odh overlay without patching
# or renaming the accelerator configs, so asserting on config/overlays/odh
# covers all three.
set -eu -o pipefail

cd "$(dirname "$0")/.."

KUSTOMIZE=${KUSTOMIZE:-kustomize}
YQ=${YQ:-yq}

OVERLAY="config/overlays/odh"
PARAMS_ENV="config/overlays/odh/params.env"
ANNOTATION="opendatahub.io/runtime-version"
BASENAME_ANNOTATION="opendatahub.io/config-base-name"
FAMILIES="nvidia-cuda amd-rocm intel-gaudi ibm-spyre"
# 9 base accelerator presets + 18 fast (-fast-1/-fast-2) variants. Update this
# count (plus a params.env key and replacement block) when adding presets.
EXPECTED_TOTAL=27
ABSENT_MARKER="__ABSENT__"

rc=0

get_param() {
  grep "^${1}=" "$PARAMS_ENV" | cut -d= -f2- || true
}

# Every dedicated version key must exist and be non-empty in params.env.
for family in $FAMILIES; do
  for suffix in "" "-fast-1" "-fast-2"; do
    key="kserve-llm-d-${family}${suffix}-upstream-version"
    if [[ -z "$(get_param "$key")" ]]; then
      echo "FAIL: $key is missing or empty in $PARAMS_ENV"
      rc=1
    fi
  done
done

# Map a rendered config name to its dedicated params.env key.
key_for() {
  local name="$1" family="" suffix=""
  case "$name" in
    *nvidia-cuda*) family="nvidia-cuda" ;;
    *amd-rocm*)    family="amd-rocm" ;;
    *intel-gaudi*) family="intel-gaudi" ;;
    *ibm-spyre*)   family="ibm-spyre" ;;
    *)             return ;;
  esac
  case "$name" in
    *-fast-1) suffix="-fast-1" ;;
    *-fast-2) suffix="-fast-2" ;;
  esac
  echo "kserve-llm-d-${family}${suffix}-upstream-version"
}

rendered=$(mktemp)
trap 'rm -f "$rendered"' EXIT
"$KUSTOMIZE" build "$OVERLAY" > "$rendered"

count=0
while IFS='|' read -r name value basename_value; do
  count=$((count + 1))
  if [[ "$value" == "$ABSENT_MARKER" ]]; then
    echo "FAIL: $name is missing the $ANNOTATION annotation"
    rc=1
    continue
  fi
  if [[ -z "$value" ]]; then
    echo "FAIL: $name has an empty $ANNOTATION annotation"
    rc=1
    continue
  fi
  if [[ "$basename_value" == "$ABSENT_MARKER" ]]; then
    echo "FAIL: $name is missing the $BASENAME_ANNOTATION annotation"
    rc=1
    continue
  fi
  if [[ "$basename_value" != "$name" ]]; then
    echo "FAIL: $name has $BASENAME_ANNOTATION '$basename_value'; it must equal the config's own unprefixed name (fast variants carry their own -fast-N name)"
    rc=1
    continue
  fi
  key=$(key_for "$name")
  if [[ -z "$key" ]]; then
    echo "FAIL: $name matches no known accelerator family; add a dedicated *-upstream-version key and replacement block, then update this check"
    rc=1
    continue
  fi
  expected=$(get_param "$key")
  if [[ "$value" != "$expected" ]]; then
    echo "FAIL: $name has $ANNOTATION '$value', expected '$expected' from params.env key $key"
    rc=1
  fi
done < <("$YQ" eval --no-doc "
  select(.kind == \"LLMInferenceServiceConfig\" and (.metadata.labels // {}).\"opendatahub.io/config-type\" == \"accelerator\")
  | .metadata.name + \"|\" + (.metadata.annotations.\"$ANNOTATION\" // \"$ABSENT_MARKER\") + \"|\" + (.metadata.annotations.\"$BASENAME_ANNOTATION\" // \"$ABSENT_MARKER\")
" "$rendered")

if [[ $count -ne $EXPECTED_TOTAL ]]; then
  echo "FAIL: expected $EXPECTED_TOTAL accelerator LLMInferenceServiceConfigs in rendered output, found $count"
  rc=1
fi

if [[ $rc -eq 0 ]]; then
  echo "verify-odh-runtime-version: OK ($count accelerator configs stamped from dedicated keys; config-base-name identity verified)"
fi

exit $rc
