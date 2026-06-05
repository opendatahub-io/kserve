#!/usr/bin/env bash
# Integration test for install_operator() -- no cluster required.
# Mocks oc, sleep, timeout, and query_catalog_info to verify the full
# resolve -> detect -> subscribe pipeline produces correct Subscription YAML.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/install-operator.sh"

RESULTS_FILE=$(mktemp)
echo "0 0" > "$RESULTS_FILE"
SUB_FILE=$(mktemp)
CLEANUP_FILE=$(mktemp)

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

assert_contains() {
    local label="$1" needle="$2" haystack="$3"
    local pass fail
    read -r pass fail < "$RESULTS_FILE"
    if [[ "$haystack" == *"$needle"* ]]; then
        echo "  PASS: $label"
        echo "$((pass + 1)) $fail" > "$RESULTS_FILE"
    else
        echo "  FAIL: $label -- expected to contain '$needle'"
        echo "$pass $((fail + 1))" > "$RESULTS_FILE"
    fi
}

assert_not_contains() {
    local label="$1" needle="$2" haystack="$3"
    local pass fail
    read -r pass fail < "$RESULTS_FILE"
    if [[ "$haystack" != *"$needle"* ]]; then
        echo "  PASS: $label"
        echo "$((pass + 1)) $fail" > "$RESULTS_FILE"
    else
        echo "  FAIL: $label -- should NOT contain '$needle'"
        echo "$pass $((fail + 1))" > "$RESULTS_FILE"
    fi
}

setup_mocks() {
    > "$SUB_FILE"
    > "$CLEANUP_FILE"

    oc() {
        case "$1" in
            apply)
                local yaml
                yaml=$(cat)
                if [[ "$yaml" == *"kind: Subscription"* ]]; then
                    echo "$yaml" > "$SUB_FILE"
                fi
                ;;
            get)
                case "$*" in
                    *catalogsource*lastObservedState*) echo "READY" ;;
                    *imagedigestmirrorset*) return 1 ;;
                    *mcp*Updating*) echo "False" ;;
                    *mcp*Updated*) echo "True" ;;
                    *subscription*installedCSV*) echo "" ;;
                    *subscription*installplan*) echo "mock-install-plan" ;;
                    *subscription*name*) echo "subscription.operators.coreos.com/mock-sub" ;;
                    *csv*phase*) echo "Succeeded" ;;
                    *deployment*) return 0 ;;
                    *jobs*) echo "" ;;
                    *packagemanifest*) echo '{"items":[]}' ;;
                    *) ;;
                esac
                ;;
            delete)
                if [[ "$*" == *subscription* ]]; then
                    echo "cleanup" >> "$CLEANUP_FILE"
                fi
                ;;
            patch|wait) ;;
            *) ;;
        esac
    }
    export -f oc

    sleep() { :; }
    export -f sleep

    timeout() { shift; "$@"; }
    export -f timeout
}

extract_field() {
    local field="$1" file="$2"
    grep "^  ${field}:" "$file" 2>/dev/null | sed "s/^  ${field}: *//" | head -1
}

# ── Case A: FBC auto-detect (no OPERATOR_VERSION) ───────────────────────
echo "Case A: FBC auto-detect -> Manual approval, no startingCSV, detected channel"
(
    setup_mocks
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.5-ea.1
    MIRROR_IMAGES=false
    source "$SCRIPT"
    query_catalog_info() { echo "beta rhods-operator.3.5.0-ea.1"; }
    export -f query_catalog_info
    install_operator >/dev/null 2>&1

    sub=$(cat "$SUB_FILE")
    assert_contains "has Subscription"          "kind: Subscription" "$sub"
    assert_eq       "channel"       "beta"      "$(extract_field channel "$SUB_FILE")"
    assert_eq       "approval"      "Manual"    "$(extract_field installPlanApproval "$SUB_FILE")"
    assert_eq       "source"        "rhods-operator-custom-catalog" "$(extract_field source "$SUB_FILE")"
    assert_not_contains "no startingCSV"        "startingCSV" "$sub"
    assert_eq "cleanup ran (stale sub)" "cleanup" "$(head -1 "$CLEANUP_FILE" 2>/dev/null || echo "")"
)

# ── Case B: Explicit version, exact match ────────────────────────────────
echo "Case B: Explicit version -> Manual approval, with startingCSV"
(
    setup_mocks
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.4.0
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.4
    MIRROR_IMAGES=false
    source "$SCRIPT"
    query_catalog_info() { echo "fast-3.x rhods-operator.3.4.0"; }
    export -f query_catalog_info
    install_operator >/dev/null 2>&1

    sub=$(cat "$SUB_FILE")
    assert_contains "has Subscription"          "kind: Subscription" "$sub"
    assert_eq       "channel"       "fast-3.x"  "$(extract_field channel "$SUB_FILE")"
    assert_eq       "approval"      "Manual"    "$(extract_field installPlanApproval "$SUB_FILE")"
    assert_contains "startingCSV"               "startingCSV: rhods-operator.3.4.0" "$sub"
)

# ── Case C: Plain OLM, no version, no FBC ───────────────────────────────
echo "Case C: Plain OLM -> Manual approval, no startingCSV, default source"
(
    setup_mocks
    OPERATOR_TYPE=odh OPERATOR_VERSION= CATALOG_SOURCE=
    MIRROR_IMAGES=false
    source "$SCRIPT"
    query_catalog_info() { echo "fast-3 opendatahub-operator.v3.2.0"; }
    export -f query_catalog_info
    install_operator >/dev/null 2>&1

    sub=$(cat "$SUB_FILE")
    assert_contains "has Subscription"          "kind: Subscription" "$sub"
    assert_eq       "channel"       "fast-3"    "$(extract_field channel "$SUB_FILE")"
    assert_eq       "approval"      "Manual" "$(extract_field installPlanApproval "$SUB_FILE")"
    assert_eq       "source"        "community-operators" "$(extract_field source "$SUB_FILE")"
    assert_not_contains "no startingCSV"        "startingCSV" "$sub"
    assert_eq "no cleanup (CI mode)" "" "$(cat "$CLEANUP_FILE" 2>/dev/null || echo "")"
)

# ── Case D: Explicit version mismatch -> error, no Subscription ─────────
echo "Case D: Version mismatch -> error, no Subscription applied"
(
    setup_mocks
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.5.0
    CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.5
    MIRROR_IMAGES=false
    source "$SCRIPT"
    query_catalog_info() { echo "beta rhods-operator.3.5.0-ea.1"; }
    export -f query_catalog_info
    exit_code=0
    ( install_operator ) >/dev/null 2>&1 || exit_code=$?
    assert_eq "exit_code" "1" "$exit_code"
    assert_eq "no Subscription applied" "" "$(cat "$SUB_FILE")"
)

# ── Case E: ODH FBC auto-detect (v-prefix handling) ─────────────────────
echo "Case E: ODH FBC auto-detect -> correct v-prefix in CSV_VERSION"
(
    setup_mocks
    OPERATOR_TYPE=odh OPERATOR_VERSION=
    CATALOG_SOURCE=quay.io/opendatahub/odh-fbc-fragment:odh-3.2
    MIRROR_IMAGES=false
    source "$SCRIPT"
    query_catalog_info() { echo "fast-3 opendatahub-operator.v3.2.0"; }
    export -f query_catalog_info
    install_operator >/dev/null 2>&1

    sub=$(cat "$SUB_FILE")
    assert_contains "has Subscription"          "kind: Subscription" "$sub"
    assert_eq       "channel"       "fast-3"    "$(extract_field channel "$SUB_FILE")"
    assert_eq       "approval"      "Manual"    "$(extract_field installPlanApproval "$SUB_FILE")"
    assert_eq       "name"          "opendatahub-operator" "$(extract_field name "$SUB_FILE")"
    assert_not_contains "no startingCSV"        "startingCSV" "$sub"
)

# ── Case F: ODH non-head version (3.3.0 in fast-3 channel) ───────────────
echo "Case F: ODH 3.3.0 (non-head) -> Manual approval, startingCSV pinned"
(
    setup_mocks
    OPERATOR_TYPE=odh OPERATOR_VERSION=3.3.0 CATALOG_SOURCE=
    MIRROR_IMAGES=false
    source "$SCRIPT"
    query_catalog_info() { echo "fast-3 opendatahub-operator.v3.3.0"; }
    export -f query_catalog_info
    install_operator >/dev/null 2>&1

    sub=$(cat "$SUB_FILE")
    assert_contains "has Subscription"          "kind: Subscription" "$sub"
    assert_eq       "channel"       "fast-3"    "$(extract_field channel "$SUB_FILE")"
    assert_eq       "approval"      "Manual"    "$(extract_field installPlanApproval "$SUB_FILE")"
    assert_eq       "source"        "community-operators" "$(extract_field source "$SUB_FILE")"
    assert_contains "startingCSV"               "startingCSV: opendatahub-operator.v3.3.0" "$sub"
)

# ── Case G: RHOAI non-head version from default catalog ──────────────────
echo "Case G: RHOAI 3.4.0 (non-head) from default catalog -> correct channel and startingCSV"
(
    setup_mocks
    OPERATOR_TYPE=rhoai OPERATOR_VERSION=3.4.0 CATALOG_SOURCE=
    MIRROR_IMAGES=false
    source "$SCRIPT"
    query_catalog_info() { echo "fast-3.x rhods-operator.3.4.0"; }
    export -f query_catalog_info
    install_operator >/dev/null 2>&1

    sub=$(cat "$SUB_FILE")
    assert_contains "has Subscription"          "kind: Subscription" "$sub"
    assert_eq       "channel"       "fast-3.x"  "$(extract_field channel "$SUB_FILE")"
    assert_eq       "approval"      "Manual"    "$(extract_field installPlanApproval "$SUB_FILE")"
    assert_eq       "source"        "redhat-operators" "$(extract_field source "$SUB_FILE")"
    assert_contains "startingCSV"               "startingCSV: rhods-operator.3.4.0" "$sub"
)

echo ""
read -r PASS FAIL < "$RESULTS_FILE"
rm -f "$RESULTS_FILE" "$SUB_FILE" "$CLEANUP_FILE"
echo "Results: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
