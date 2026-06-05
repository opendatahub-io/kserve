#!/usr/bin/env bash
# Unit test for detect_channel() -- no cluster required.
# Mocks query_catalog_info to verify version detection logic.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/install-operator.sh"

RESULTS_FILE=$(mktemp)
echo "0 0" > "$RESULTS_FILE"

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    local pass fail
    read -r pass fail < "$RESULTS_FILE"
    if [[ "$expected" == "$actual" ]]; then
        echo "  PASS: $label"
        echo "$((pass + 1)) $fail" > "$RESULTS_FILE"
    else
        echo "  FAIL: $label -- expected '$expected', got '$actual'"
        echo "$pass $((fail + 1))" > "$RESULTS_FILE"
    fi
}

oc() { :; }
export -f oc

# ── Case 1: Explicit OPERATOR_VERSION, exact match ──────────────────────
echo "Case 1: Explicit OPERATOR_VERSION, exact match"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.4.0
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.4
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "fast-3.x rhods-operator.3.4.0"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "fast-3.x" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION unchanged" "3.4.0" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ── Case 2: Explicit OPERATOR_VERSION, mismatch -> hard error ───────────
echo "Case 2: Explicit OPERATOR_VERSION, mismatch -> hard error"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.5.0
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.5
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "beta rhods-operator.3.5.0-ea.1"; }
    export -f query_catalog_info
    exit_code=0
    output=$(detect_channel 2>&1) || exit_code=$?
    assert_eq "exit_code" "1" "$exit_code"
    assert_eq "output contains ERROR" "yes" \
        "$([[ "$output" == *ERROR* ]] && echo yes || echo no)"
)

# ── Case 3: FBC tag inference, no explicit version ───────────────────────
echo "Case 3: FBC tag inference (no OPERATOR_VERSION)"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.5-ea.1
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "beta rhods-operator.3.5.0-ea.1"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "beta" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION auto-detected" "3.5.0-ea.1" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "false" "$USE_STARTING_CSV"
)

# ── Case 4: No version, no FBC -- plain OLM resolve ─────────────────────
echo "Case 4: No version, no FBC (plain OLM resolve)"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION= CATALOG_SOURCE=
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "fast-3 opendatahub-operator.v3.2.0"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "fast-3" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION stays empty" "" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ── Case 5: FBC tag with no parseable version ────────────────────────────
echo "Case 5: FBC :latest tag (no version to parse, falls back to default)"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:latest
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "stable-3.x rhods-operator.3.3.2"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "stable-3.x" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION stays empty" "" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ── Case 6: ODH FBC tag inference ────────────────────────────────────────
echo "Case 6: ODH FBC tag inference (odh- prefix)"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=
    CATALOG_SOURCE=quay.io/opendatahub/odh-fbc-fragment:odh-3.2
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "fast-3 opendatahub-operator.v3.2.0"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "fast-3" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION auto-detected" "3.2.0" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "false" "$USE_STARTING_CSV"
)

# ── Case 7: CatalogSource by name (not an image), no version ────────────
echo "Case 7: CatalogSource by name (not an image), no version"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=
    CATALOG_SOURCE=my-custom-catalogsource
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "stable-3.x rhods-operator.3.3.2"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "stable-3.x" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION stays empty" "" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ── Case 8: Digest reference (@sha256:...) -- no tag to parse ────────────
echo "Case 8: Digest reference (no tag, no inference)"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment@sha256:abcdef1234567890
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "stable-3.x rhods-operator.3.3.2"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "stable-3.x" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION stays empty" "" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ── Case 9: Catalog returns empty (catalog not ready) ───────────────────
echo "Case 9: Catalog returns empty (fallback to default channel)"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.5-ea.1
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo ""; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "fast-3.x" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION stays empty" "" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ── Case 10: ODH explicit version, mismatch -> hard error ───────────────
echo "Case 10: ODH explicit version with v-prefix, mismatch -> hard error"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=3.3.0
    CATALOG_SOURCE=quay.io/opendatahub/odh-fbc-fragment:odh-3.3
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "fast-3 opendatahub-operator.v3.2.0"; }
    export -f query_catalog_info
    exit_code=0
    output=$(detect_channel 2>&1) || exit_code=$?
    assert_eq "exit_code" "1" "$exit_code"
    assert_eq "output contains ERROR" "yes" \
        "$([[ "$output" == *ERROR* ]] && echo yes || echo no)"
)

# ── Case 11: ODH explicit non-head version found via entries ─────────────
echo "Case 11: ODH 3.3.0 (non-head) found in fast-3 channel entries"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=3.3.0 CATALOG_SOURCE=
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "fast-3 opendatahub-operator.v3.3.0"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "fast-3" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION unchanged" "3.3.0" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ── Case 12: RHOAI explicit non-head version found via entries ───────────
echo "Case 12: RHOAI 3.4.0 (non-head) found in fast-3.x when head is 3.5.0"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.4.0 CATALOG_SOURCE=
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "fast-3.x rhods-operator.3.4.0"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "fast-3.x" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION unchanged" "3.4.0" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ── Case 13: ODH explicit version, truly not in any channel ──────────────
echo "Case 13: ODH 9.9.9 not in any channel -> hard error"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=9.9.9 CATALOG_SOURCE=
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "fast-3 opendatahub-operator.v3.4.0"; }
    export -f query_catalog_info
    exit_code=0
    output=$(detect_channel 2>&1) || exit_code=$?
    assert_eq "exit_code" "1" "$exit_code"
    assert_eq "output contains ERROR" "yes" \
        "$([[ "$output" == *ERROR* ]] && echo yes || echo no)"
)

# ── Case 14: RHOAI EA version from FBC fragment, non-head ────────────────
echo "Case 14: RHOAI 3.5.0-ea.1 explicit version, found as non-head entry"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.5.0-ea.1
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.5-ea.1
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="test-catalog"
    query_catalog_info() { echo "beta rhods-operator.3.5.0-ea.1"; }
    export -f query_catalog_info
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "beta" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION unchanged" "3.5.0-ea.1" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

# ═══════════════════════════════════════════════════════════════════════
# query_catalog_info tests -- exercise the real function with mocked oc
# ═══════════════════════════════════════════════════════════════════════

ODH_CATALOG_JSON='{
  "items": [{
    "metadata": {"name": "opendatahub-operator"},
    "status": {
      "catalogSource": "community-operators",
      "defaultChannel": "fast-3",
      "channels": [
        {
          "name": "fast-3",
          "currentCSV": "opendatahub-operator.v3.4.0",
          "entries": [
            {"name": "opendatahub-operator.v3.4.0"},
            {"name": "opendatahub-operator.v3.3.0"},
            {"name": "opendatahub-operator.v3.2.0"},
            {"name": "opendatahub-operator.v3.1.0"}
          ]
        },
        {
          "name": "fast",
          "currentCSV": "opendatahub-operator.v2.35.0",
          "entries": [
            {"name": "opendatahub-operator.v2.35.0"},
            {"name": "opendatahub-operator.v2.34.0"}
          ]
        }
      ]
    }
  }]
}'

RHOAI_CATALOG_JSON='{
  "items": [{
    "metadata": {"name": "rhods-operator"},
    "status": {
      "catalogSource": "rhods-operator-custom-catalog",
      "defaultChannel": "beta",
      "channels": [
        {
          "name": "beta",
          "currentCSV": "rhods-operator.3.5.0-ea.1",
          "entries": [
            {"name": "rhods-operator.3.5.0-ea.1"},
            {"name": "rhods-operator.3.4.0"},
            {"name": "rhods-operator.3.3.0"}
          ]
        },
        {
          "name": "fast-3.x",
          "currentCSV": "rhods-operator.3.4.0",
          "entries": [
            {"name": "rhods-operator.3.4.0"},
            {"name": "rhods-operator.3.3.0"}
          ]
        }
      ]
    }
  }]
}'

# ── Case 15: query_catalog_info finds ODH head version ───────────────────
echo "Case 15: query_catalog_info: ODH 3.4.0 (head of fast-3)"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=3.4.0
    source "$SCRIPT"; resolve_operator_vars
    oc() { echo "$ODH_CATALOG_JSON"; }; export -f oc
    result=$(query_catalog_info "community-operators" "opendatahub-operator.v3.4.0")
    assert_eq "channel" "fast-3" "$(echo "$result" | awk '{print $1}')"
    assert_eq "csv" "opendatahub-operator.v3.4.0" "$(echo "$result" | awk '{print $2}')"
)

# ── Case 16: query_catalog_info finds ODH non-head version via entries ───
echo "Case 16: query_catalog_info: ODH 3.3.0 (non-head in fast-3 entries)"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=3.3.0
    source "$SCRIPT"; resolve_operator_vars
    oc() { echo "$ODH_CATALOG_JSON"; }; export -f oc
    result=$(query_catalog_info "community-operators" "opendatahub-operator.v3.3.0")
    assert_eq "channel" "fast-3" "$(echo "$result" | awk '{print $1}')"
    assert_eq "csv" "opendatahub-operator.v3.3.0" "$(echo "$result" | awk '{print $2}')"
)

# ── Case 17: query_catalog_info finds ODH 3.2.0 via entries ─────────────
echo "Case 17: query_catalog_info: ODH 3.2.0 (older non-head in fast-3)"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=3.2.0
    source "$SCRIPT"; resolve_operator_vars
    oc() { echo "$ODH_CATALOG_JSON"; }; export -f oc
    result=$(query_catalog_info "community-operators" "opendatahub-operator.v3.2.0")
    assert_eq "channel" "fast-3" "$(echo "$result" | awk '{print $1}')"
    assert_eq "csv" "opendatahub-operator.v3.2.0" "$(echo "$result" | awk '{print $2}')"
)

# ── Case 18: query_catalog_info finds RHOAI head in beta ─────────────────
echo "Case 18: query_catalog_info: RHOAI 3.5.0-ea.1 (head of beta)"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.5.0-ea.1
    source "$SCRIPT"; resolve_operator_vars
    oc() { echo "$RHOAI_CATALOG_JSON"; }; export -f oc
    result=$(query_catalog_info "rhods-operator-custom-catalog" "rhods-operator.3.5.0-ea.1")
    assert_eq "channel" "beta" "$(echo "$result" | awk '{print $1}')"
    assert_eq "csv" "rhods-operator.3.5.0-ea.1" "$(echo "$result" | awk '{print $2}')"
)

# ── Case 19: query_catalog_info finds RHOAI non-head in beta entries ─────
echo "Case 19: query_catalog_info: RHOAI 3.4.0 (non-head in beta entries)"
(
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.4.0
    source "$SCRIPT"; resolve_operator_vars
    oc() { echo "$RHOAI_CATALOG_JSON"; }; export -f oc
    result=$(query_catalog_info "rhods-operator-custom-catalog" "rhods-operator.3.4.0")
    channel=$(echo "$result" | awk '{print $1}')
    csv=$(echo "$result" | awk '{print $2}')
    assert_eq "csv" "rhods-operator.3.4.0" "$csv"
    assert_eq "channel is beta or fast-3.x" "yes" \
        "$([[ "$channel" == "beta" || "$channel" == "fast-3.x" ]] && echo yes || echo no)"
)

# ── Case 20: query_catalog_info returns nothing for unknown version ──────
echo "Case 20: query_catalog_info: ODH 9.9.9 not found anywhere"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=9.9.9
    source "$SCRIPT"; resolve_operator_vars
    oc() { echo "$ODH_CATALOG_JSON"; }; export -f oc
    result=$(query_catalog_info "community-operators" "opendatahub-operator.v9.9.9")
    assert_eq "falls back to default channel" "fast-3" "$(echo "$result" | awk '{print $1}')"
    assert_eq "returns head csv" "opendatahub-operator.v3.4.0" "$(echo "$result" | awk '{print $2}')"
)

# ── Case 21: Full pipeline -- ODH 3.3.0 with real query_catalog_info ────
echo "Case 21: Full pipeline: ODH 3.3.0 from community-operators (non-head)"
(
    OPERATOR_TYPE=odh OPERATOR_VERSION=3.3.0 CATALOG_SOURCE=
    source "$SCRIPT"; resolve_operator_vars; OPERATOR_SOURCE="community-operators"
    oc() { echo "$ODH_CATALOG_JSON"; }; export -f oc
    detect_channel
    assert_eq "OPERATOR_CHANNEL" "fast-3" "$OPERATOR_CHANNEL"
    assert_eq "OPERATOR_VERSION unchanged" "3.3.0" "$OPERATOR_VERSION"
    assert_eq "USE_STARTING_CSV" "true" "$USE_STARTING_CSV"
)

echo ""
read -r PASS FAIL < "$RESULTS_FILE"
rm -f "$RESULTS_FILE"
echo "Results: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
