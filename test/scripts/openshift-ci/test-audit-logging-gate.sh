#!/usr/bin/env bash
# Unit test for the LLMInferenceService audit logging gate -- no cluster required.
# Mocks oc to verify resolve_audit_logging_enabled() (version.sh) and
# get_subscription_installed_version() (common.sh) across the mode x RHCL version x CRD matrix.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

RESULTS_FILE=$(mktemp)
echo "0 0" > "$RESULTS_FILE"
OUT_FILE=$(mktemp)

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

# Mock knobs, set per case before setup_oc_mock:
#   MOCK_INSTALLED_CSV   installedCSV of the Subscription; empty = Subscription missing (oc fails)
#   MOCK_CSV_VERSION     CSV spec.version; empty = exercise the name-derived fallback
#   MOCK_AUTHORINO_FIELD non-empty = Authorino CRD declares spec.enableLoggingFields
#   MOCK_TELEMETRY_FIELD non-empty = TelemetryPolicy CRD declares spec.logging
setup_oc_mock() {
    oc() {
        case "$*" in
            *"get subscription"*installedCSV*)
                [[ -n "${MOCK_INSTALLED_CSV}" ]] || return 1
                echo "${MOCK_INSTALLED_CSV}" ;;
            *"get csv"*spec.version*) echo "${MOCK_CSV_VERSION}" ;;
            *"get crd authorinos."*enableLoggingFields*) echo "${MOCK_AUTHORINO_FIELD}" ;;
            *"get crd telemetrypolicies."*logging*) echo "${MOCK_TELEMETRY_FIELD}" ;;
            *) return 0 ;;
        esac
    }
    export -f oc
}

# load_helpers: source common.sh then version.sh (once per subshell; common.sh has readonly vars).
load_helpers() {
    source "$SCRIPT_DIR/common.sh"
    source "$SCRIPT_DIR/version.sh"
}

# run_gate <mode> <floor> [sub] [ns]: call the gate, keep its globals, capture output and rc.
run_gate() {
    rc=0
    resolve_audit_logging_enabled "$@" > "$OUT_FILE" 2>&1 || rc=$?
    out=$(cat "$OUT_FILE")
}

# ── Case 1: RHCL 1.4.3 without the CRDs, auto -> skip (today's CI) ───────
echo "Case 1: 1.4.3, no CRD fields, auto -> skip"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.4.3 MOCK_CSV_VERSION=1.4.3
    MOCK_AUTHORINO_FIELD="" MOCK_TELEMETRY_FIELD=""
    load_helpers; setup_oc_mock
    run_gate auto 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "0" "$rc"
    assert_eq "AUDIT_LOGGING_ENABLED" "false" "$AUDIT_LOGGING_ENABLED"
    assert_eq "RHCL_INSTALLED_VERSION" "1.4.3" "$RHCL_INSTALLED_VERSION"
    assert_contains "skip line" "skipping LLMInferenceService audit logging setup" "$out"
    assert_contains "mentions version and floor" "installed RHCL 1.4.3 is below the minimum 1.5.1" "$out"
    assert_contains "mentions missing fields" "has no TelemetryPolicy logging support (missing: Authorino.spec.enableLoggingFields, TelemetryPolicy.spec.logging)" "$out"
)

# ── Case 2: 1.5.0 with the CRDs, auto -> blocked by the floor only ───────
echo "Case 2: 1.5.0, CRD fields present, auto -> blocked by floor"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.0 MOCK_CSV_VERSION=1.5.0
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate auto 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "0" "$rc"
    assert_eq "AUDIT_LOGGING_ENABLED" "false" "$AUDIT_LOGGING_ENABLED"
    assert_contains "below floor" "installed RHCL 1.5.0 is below the minimum 1.5.1" "$out"
    assert_not_contains "no CRD complaint" "has no TelemetryPolicy logging support" "$out"
)

# ── Case 3: 1.5.0 with the CRDs, true -> hard error ──────────────────────
echo "Case 3: 1.5.0, CRD fields present, true -> error"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.0 MOCK_CSV_VERSION=1.5.0
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate true 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "1" "$rc"
    assert_eq "AUDIT_LOGGING_ENABLED" "false" "$AUDIT_LOGGING_ENABLED"
    assert_contains "ERROR line" "ERROR: LLMISVC_AUDIT_LOGGING=true but installed RHCL 1.5.0 is below the minimum 1.5.1" "$out"
)

# ── Case 4: 1.5.1 with the CRDs, auto and true -> enabled ────────────────
echo "Case 4: 1.5.1, CRD fields present, auto -> enabled"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.1 MOCK_CSV_VERSION=1.5.1
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate auto 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "0" "$rc"
    assert_eq "AUDIT_LOGGING_ENABLED" "true" "$AUDIT_LOGGING_ENABLED"
    assert_contains "prerequisites met" "prerequisites met: RHCL 1.5.1 (minimum 1.5.1)" "$out"
)
echo "Case 5: 1.5.1, CRD fields present, true -> enabled"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.1 MOCK_CSV_VERSION=1.5.1
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate true 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "0" "$rc"
    assert_eq "AUDIT_LOGGING_ENABLED" "true" "$AUDIT_LOGGING_ENABLED"
)

# ── Case 6: 1.6.0 -> enabled (floor is a minimum, not an exact match) ────
echo "Case 6: 1.6.0, CRD fields present, auto -> enabled"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.6.0 MOCK_CSV_VERSION=1.6.0
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate auto 1.5.1 rhcl-operator kuadrant-system
    assert_eq "AUDIT_LOGGING_ENABLED" "true" "$AUDIT_LOGGING_ENABLED"
)

# ── Case 7: floor empty + upstream rc CSV -> enabled on CRDs alone ───────
echo "Case 7: floor empty, kuadrant-operator.v1.6.0-rc4, CRD fields present -> enabled"
(
    MOCK_INSTALLED_CSV=kuadrant-operator.v1.6.0-rc4 MOCK_CSV_VERSION=1.6.0-rc4
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate auto "" kuadrant-operator kuadrant-system
    assert_eq "rc" "0" "$rc"
    assert_eq "AUDIT_LOGGING_ENABLED" "true" "$AUDIT_LOGGING_ENABLED"
    assert_eq "RHCL_INSTALLED_VERSION" "1.6.0-rc4" "$RHCL_INSTALLED_VERSION"
    assert_contains "minimum disabled" "audit logging minimum disabled" "$out"
)

# ── Case 8: upstream nightly 0.0.0: blocked with the floor, enabled without ──
echo "Case 8: kuadrant-operator.v0.0.0 with floor 1.5.1 -> blocked"
(
    MOCK_INSTALLED_CSV=kuadrant-operator.v0.0.0 MOCK_CSV_VERSION=0.0.0
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate auto 1.5.1 kuadrant-operator kuadrant-system
    assert_eq "AUDIT_LOGGING_ENABLED" "false" "$AUDIT_LOGGING_ENABLED"
    assert_contains "below floor" "installed RHCL 0.0.0 is below the minimum 1.5.1" "$out"
)
echo "Case 9: kuadrant-operator.v0.0.0 with empty floor -> enabled"
(
    MOCK_INSTALLED_CSV=kuadrant-operator.v0.0.0 MOCK_CSV_VERSION=0.0.0
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate auto "" kuadrant-operator kuadrant-system
    assert_eq "AUDIT_LOGGING_ENABLED" "true" "$AUDIT_LOGGING_ENABLED"
)

# ── Case 10: Subscription missing -> version unknown, fail closed ─────────
echo "Case 10: Subscription missing, auto -> skip as unknown"
(
    MOCK_INSTALLED_CSV="" MOCK_CSV_VERSION=""
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate auto 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "0" "$rc"
    assert_eq "AUDIT_LOGGING_ENABLED" "false" "$AUDIT_LOGGING_ENABLED"
    assert_eq "RHCL_INSTALLED_VERSION empty" "" "$RHCL_INSTALLED_VERSION"
    assert_contains "unknown reason" "the installed RHCL version is unknown" "$out"
    assert_contains "escape hatch named" "set RHCL_AUDIT_LOGGING_MIN_VERSION= to skip the version check" "$out"
)
echo "Case 11: Subscription missing, true -> error"
(
    MOCK_INSTALLED_CSV="" MOCK_CSV_VERSION=""
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate true 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "1" "$rc"
    assert_contains "ERROR line" "ERROR: LLMISVC_AUDIT_LOGGING=true but the installed RHCL version is unknown" "$out"
)

# ── Case 12: CSV without spec.version -> name-derived fallback strips "v" ─
echo "Case 12: rhcl-operator.v1.5.1 without spec.version -> 1.5.1 from the CSV name"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.1 MOCK_CSV_VERSION=""
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    assert_eq "helper output" "1.5.1" "$(get_subscription_installed_version rhcl-operator kuadrant-system)"
    run_gate auto 1.5.1 rhcl-operator kuadrant-system
    assert_eq "AUDIT_LOGGING_ENABLED" "true" "$AUDIT_LOGGING_ENABLED"
)

# ── Case 13: only one CRD field present, true -> error names only the missing one ──
echo "Case 13: 1.5.1, only Authorino field present, true -> error names TelemetryPolicy.spec.logging"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.1 MOCK_CSV_VERSION=1.5.1
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=""
    load_helpers; setup_oc_mock
    run_gate true 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "1" "$rc"
    assert_contains "missing field named" "(missing: TelemetryPolicy.spec.logging)" "$out"
    assert_not_contains "present field not named" "enableLoggingFields," "$out"
    assert_not_contains "no version complaint" "below the minimum" "$out"
)

# ── Case 14: mode false -> silent no-op; bogus mode -> error ─────────────
echo "Case 14: mode false -> silent"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.1 MOCK_CSV_VERSION=1.5.1
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate false 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "0" "$rc"
    assert_eq "AUDIT_LOGGING_ENABLED" "false" "$AUDIT_LOGGING_ENABLED"
    assert_eq "no output" "" "$out"
)
echo "Case 15: mode bogus -> error"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.1 MOCK_CSV_VERSION=1.5.1
    MOCK_AUTHORINO_FIELD=map MOCK_TELEMETRY_FIELD=map
    load_helpers; setup_oc_mock
    run_gate bogus 1.5.1 rhcl-operator kuadrant-system
    assert_eq "rc" "1" "$rc"
    assert_contains "usage error" "must be auto, true or false (got 'bogus')" "$out"
)

# ── Case 16: get_subscription_installed_version edge cases ────────────────
echo "Case 16: helper returns spec.version and rejects garbage"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.4.3 MOCK_CSV_VERSION=1.4.3
    MOCK_AUTHORINO_FIELD="" MOCK_TELEMETRY_FIELD=""
    load_helpers; setup_oc_mock
    assert_eq "spec.version wins" "1.4.3" "$(get_subscription_installed_version rhcl-operator kuadrant-system)"
    MOCK_INSTALLED_CSV=rhcl-operator.vgarbage MOCK_CSV_VERSION=garbage
    rc=0; got=$(get_subscription_installed_version rhcl-operator kuadrant-system) || rc=$?
    assert_eq "garbage rc" "1" "$rc"
    assert_eq "garbage prints nothing" "" "$got"
    MOCK_INSTALLED_CSV=rhcl-operator.v1.5.1-ea.1 MOCK_CSV_VERSION=1.5.1-ea.1
    assert_eq "pre-release passes through" "1.5.1-ea.1" "$(get_subscription_installed_version rhcl-operator kuadrant-system)"
)

# ── Case 17: diagnostics print the installed CSV, version and floor ───────
echo "Case 17: print_audit_logging_diagnostics shows CSV, version and floor"
(
    MOCK_INSTALLED_CSV=rhcl-operator.v1.4.3 MOCK_CSV_VERSION=1.4.3
    MOCK_AUTHORINO_FIELD="" MOCK_TELEMETRY_FIELD=""
    load_helpers; setup_oc_mock
    out=$(print_audit_logging_diagnostics 2>&1)
    assert_contains "installed CSV and version" "installedCSV=rhcl-operator.v1.4.3 version=1.4.3" "$out"
    assert_contains "default floor" "minimum RHCL version=1.5.1" "$out"
    RHCL_AUDIT_LOGGING_MIN_VERSION=""
    out=$(print_audit_logging_diagnostics 2>&1)
    assert_contains "empty floor shown as disabled" "minimum RHCL version=disabled" "$out"
)

echo ""
read -r PASS FAIL < "$RESULTS_FILE"
rm -f "$RESULTS_FILE" "$OUT_FILE"
echo "Results: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
