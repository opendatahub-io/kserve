#!/bin/bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACES=("opendatahub" "openshift-operators")

# Use same output dir as test-upgrade-service-impact.sh
resolve_output_dir() {
  if [[ -n "${UPGRADE_TEST_OUTPUT_DIR:-}" ]]; then
    echo "$UPGRADE_TEST_OUTPUT_DIR"
  elif [[ -f "${SCRIPT_DIR}/.current-output-dir" ]]; then
    cat "${SCRIPT_DIR}/.current-output-dir"
  else
    echo "${SCRIPT_DIR}"
  fi
}
SNAPSHOT_BASE="$(resolve_output_dir)/snapshots"

usage() {
    cat <<EOF
Usage:
  $0 snapshot --name <snapshot-name>
  $0 diff --before <name> --after <name>

Commands:
  snapshot   Capture cluster state into snapshots/<name>/
  diff       Compare two snapshots and print a diff report

Examples:
  $0 snapshot --name pre-upgrade
  $0 snapshot --name post-upgrade
  $0 diff --before pre-upgrade --after post-upgrade
EOF
    exit 1
}

strip_dynamic_fields() {
    yq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.generation,
            .metadata.creationTimestamp, .metadata.managedFields,
            .metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"],
            .status.conditions[].lastTransitionTime,
            .status.conditions[].lastProbeTime)' 2>/dev/null || cat
}

snapshot_objects_list() {
    local dir=$1
    local out="${dir}/objects.txt"
    > "$out"
    for ns in "${NAMESPACES[@]}"; do
        oc get deploy,svc,configmap,statefulset,secret -n "$ns" \
            -o custom-columns="KIND:.kind,NAME:.metadata.name" --no-headers 2>/dev/null \
            | sed "s/^/${ns}\t/" >> "$out" || true
    done
    echo "  objects.txt ($(wc -l < "$out") items)"
}

snapshot_deployments() {
    local dir=$1
    local out="${dir}/deployments"
    mkdir -p "$out"
    for ns in "${NAMESPACES[@]}"; do
        for dep in $(oc get deploy -n "$ns" -o name 2>/dev/null); do
            local name="${ns}_$(basename "$dep")"
            oc get "$dep" -n "$ns" -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/${name}.yaml"
        done
    done
    echo "  deployments/ ($(ls "$out" | wc -l) files)"
}

snapshot_services() {
    local dir=$1
    local out="${dir}/services"
    mkdir -p "$out"
    for ns in "${NAMESPACES[@]}"; do
        for svc in $(oc get svc -n "$ns" -o name 2>/dev/null); do
            local name="${ns}_$(basename "$svc")"
            oc get "$svc" -n "$ns" -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/${name}.yaml"
        done
    done
    echo "  services/ ($(ls "$out" | wc -l) files)"
}

snapshot_configmaps() {
    local dir=$1
    local out="${dir}/configmaps"
    mkdir -p "$out"
    for ns in "${NAMESPACES[@]}"; do
        for cm in $(oc get configmap -n "$ns" -o name 2>/dev/null); do
            local name="${ns}_$(basename "$cm")"
            oc get "$cm" -n "$ns" -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/${name}.yaml"
        done
    done
    echo "  configmaps/ ($(ls "$out" | wc -l) files)"
}

snapshot_pods() {
    local dir=$1
    local out="${dir}/pods.json"
    local items="[]"
    for ns in "${NAMESPACES[@]}"; do
        local ns_pods
        ns_pods=$(oc get pods -n "$ns" -o json 2>/dev/null | \
            jq "[.items[] | {
                namespace: .metadata.namespace,
                name: .metadata.name,
                images: [.spec.containers[].image],
                restartCounts: [.status.containerStatuses[]? // [] | .restartCount],
                ready: ([.status.containerStatuses[]? // [] | .ready] | all),
                phase: .status.phase
            }]" 2>/dev/null || echo "[]")
        items=$(echo "$items" "$ns_pods" | jq -s '.[0] + .[1]')
    done
    echo "$items" | jq '.' > "$out"
    echo "  pods.json ($(echo "$items" | jq length) pods)"
}

snapshot_crs() {
    local dir=$1
    local out="${dir}/crs"
    mkdir -p "$out"
    oc get datasciencecluster -A -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/datasciencecluster.yaml" || true
    oc get dscinitialization -A -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/dscinitialization.yaml" || true
    oc get kserve -A -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/kserve.yaml" || true
    echo "  crs/ (DSC, DSCI, Kserve)"
}

snapshot_serving() {
    local dir=$1
    local out="${dir}/serving"
    mkdir -p "$out"
    oc get inferenceservice -A -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/inferenceservices.yaml" || true
    oc get llminferenceservice -A -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/llminferenceservices.yaml" || true
    oc get servingruntime -A -o yaml 2>/dev/null | strip_dynamic_fields > "${out}/servingruntimes.yaml" || true
    echo "  serving/ (ISVC, LLMISVC, ServingRuntime)"
}

snapshot_csv() {
    local dir=$1
    local out="${dir}/csv.yaml"
    oc get csv -n openshift-operators -o yaml 2>/dev/null \
        | yq '.items[] | select(.metadata.name | test("opendatahub"))' \
        | strip_dynamic_fields > "$out" 2>/dev/null || true
    echo "  csv.yaml"
}

do_snapshot() {
    local name=""
    while [[ $# -gt 0 ]]; do
        case $1 in
            --name) name="$2"; shift 2 ;;
            *) echo "Unknown option: $1"; usage ;;
        esac
    done
    [[ -z "$name" ]] && { echo "Error: --name is required"; usage; }

    local dir="${SNAPSHOT_BASE}/${name}"
    rm -rf "$dir"
    mkdir -p "$dir"

    echo "Taking snapshot '${name}' → ${dir}/"
    echo "Namespaces: ${NAMESPACES[*]}"
    echo "Timestamp: $(date -Iseconds)" > "${dir}/metadata.txt"
    echo ""

    snapshot_objects_list "$dir"
    snapshot_deployments "$dir"
    snapshot_services "$dir"
    snapshot_configmaps "$dir"
    snapshot_pods "$dir"
    snapshot_crs "$dir"
    snapshot_serving "$dir"
    snapshot_csv "$dir"

    echo ""
    echo "Snapshot '${name}' saved to ${dir}/"
}

diff_directory() {
    local label=$1 before_dir=$2 after_dir=$3
    local before_files after_files

    [[ ! -d "$before_dir" ]] && { echo "  (no ${label}/ in before snapshot)"; return; }
    [[ ! -d "$after_dir" ]] && { echo "  (no ${label}/ in after snapshot)"; return; }

    before_files=$(ls "$before_dir" 2>/dev/null | sort)
    after_files=$(ls "$after_dir" 2>/dev/null | sort)

    local added removed
    added=$(comm -13 <(echo "$before_files") <(echo "$after_files"))
    removed=$(comm -23 <(echo "$before_files") <(echo "$after_files"))
    local common
    common=$(comm -12 <(echo "$before_files") <(echo "$after_files"))

    if [[ -n "$added" ]]; then
        echo ""
        echo "  ADDED ${label}:"
        echo "$added" | while read -r f; do echo "    + $f"; done
    fi
    if [[ -n "$removed" ]]; then
        echo ""
        echo "  REMOVED ${label}:"
        echo "$removed" | while read -r f; do echo "    - $f"; done
    fi

    local modified_count=0
    for f in $common; do
        if ! diff -q "${before_dir}/${f}" "${after_dir}/${f}" > /dev/null 2>&1; then
            if [[ $modified_count -eq 0 ]]; then
                echo ""
                echo "  MODIFIED ${label}:"
            fi
            echo "    ~ $f"
            diff --color=auto -u "${before_dir}/${f}" "${after_dir}/${f}" 2>/dev/null \
                | head -30 | sed 's/^/      /'
            modified_count=$((modified_count + 1))
        fi
    done

    if [[ -z "$added" && -z "$removed" && $modified_count -eq 0 ]]; then
        echo "  ${label}: no changes"
    fi
}

diff_pods() {
    local before=$1 after=$2

    [[ ! -f "$before" || ! -f "$after" ]] && { echo "  (pods.json missing)"; return; }

    echo ""
    echo "  RESTARTED pods:"
    jq -n --slurpfile b "$before" --slurpfile a "$after" '
        [$a[0][] as $ap |
         ($b[0][] | select(.name == $ap.name and .namespace == $ap.namespace)) as $bp |
         select($bp != null) |
         select(($ap.restartCounts | add // 0) > ($bp.restartCounts | add // 0)) |
         {name: $ap.name, namespace: $ap.namespace,
          before: ($bp.restartCounts | add // 0),
          after: ($ap.restartCounts | add // 0)}
        ]' 2>/dev/null | jq -r '.[] | "    \(.namespace)/\(.name): \(.before) → \(.after) restarts"'

    echo ""
    echo "  IMAGE_CHANGED pods:"
    jq -n --slurpfile b "$before" --slurpfile a "$after" '
        [$a[0][] as $ap |
         ($b[0][] | select(.name == $ap.name and .namespace == $ap.namespace)) as $bp |
         select($bp != null) |
         select($ap.images != $bp.images) |
         {name: $ap.name, namespace: $ap.namespace,
          before: $bp.images, after: $ap.images}
        ]' 2>/dev/null | jq -r '.[] | "    \(.namespace)/\(.name):\n      before: \(.before | join(", "))\n      after:  \(.after | join(", "))"'

    echo ""
    echo "  NEW pods (after only):"
    jq -n --slurpfile b "$before" --slurpfile a "$after" '
        [$a[0][] |
         select(. as $ap | $b[0] | map(select(.name == $ap.name and .namespace == $ap.namespace)) | length == 0) |
         .namespace + "/" + .name
        ]' 2>/dev/null | jq -r '.[] | "    + \(.)"'

    echo ""
    echo "  GONE pods (before only):"
    jq -n --slurpfile b "$before" --slurpfile a "$after" '
        [$b[0][] |
         select(. as $bp | $a[0] | map(select(.name == $bp.name and .namespace == $bp.namespace)) | length == 0) |
         .namespace + "/" + .name
        ]' 2>/dev/null | jq -r '.[] | "    - \(.)"'
}

do_diff() {
    local before="" after=""
    while [[ $# -gt 0 ]]; do
        case $1 in
            --before) before="$2"; shift 2 ;;
            --after) after="$2"; shift 2 ;;
            *) echo "Unknown option: $1"; usage ;;
        esac
    done
    [[ -z "$before" || -z "$after" ]] && { echo "Error: --before and --after are required"; usage; }

    local before_dir="${SNAPSHOT_BASE}/${before}"
    local after_dir="${SNAPSHOT_BASE}/${after}"
    [[ ! -d "$before_dir" ]] && { echo "Error: snapshot '${before}' not found at ${before_dir}"; exit 1; }
    [[ ! -d "$after_dir" ]] && { echo "Error: snapshot '${after}' not found at ${after_dir}"; exit 1; }

    echo "=========================================="
    echo " Upgrade Diff Report"
    echo "=========================================="
    echo " Before: ${before} ($(head -1 "${before_dir}/metadata.txt" 2>/dev/null || echo 'unknown'))"
    echo " After:  ${after} ($(head -1 "${after_dir}/metadata.txt" 2>/dev/null || echo 'unknown'))"
    echo "=========================================="

    echo ""
    echo "--- Deployments ---"
    diff_directory "deployments" "${before_dir}/deployments" "${after_dir}/deployments"

    echo ""
    echo "--- Services ---"
    diff_directory "services" "${before_dir}/services" "${after_dir}/services"

    echo ""
    echo "--- ConfigMaps ---"
    diff_directory "configmaps" "${before_dir}/configmaps" "${after_dir}/configmaps"

    echo ""
    echo "--- CRs (DSC, DSCI, Kserve) ---"
    diff_directory "crs" "${before_dir}/crs" "${after_dir}/crs"

    echo ""
    echo "--- Serving (ISVC, LLMISVC, ServingRuntime) ---"
    diff_directory "serving" "${before_dir}/serving" "${after_dir}/serving"

    echo ""
    echo "--- CSV ---"
    if [[ -f "${before_dir}/csv.yaml" && -f "${after_dir}/csv.yaml" ]]; then
        if ! diff -q "${before_dir}/csv.yaml" "${after_dir}/csv.yaml" > /dev/null 2>&1; then
            echo "  CSV MODIFIED:"
            diff --color=auto -u "${before_dir}/csv.yaml" "${after_dir}/csv.yaml" 2>/dev/null \
                | head -50 | sed 's/^/    /'
        else
            echo "  csv: no changes"
        fi
    fi

    echo ""
    echo "--- Pods ---"
    diff_pods "${before_dir}/pods.json" "${after_dir}/pods.json"

    echo ""
    echo "=========================================="
    echo " End of Report"
    echo "=========================================="
}

[[ $# -eq 0 ]] && usage

case $1 in
    snapshot) shift; do_snapshot "$@" ;;
    diff) shift; do_diff "$@" ;;
    *) usage ;;
esac
