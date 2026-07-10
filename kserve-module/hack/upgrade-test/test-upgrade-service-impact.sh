#!/bin/bash
# Upgrade service impact test orchestrator.
#
# Usage:
#   ./test-upgrade-service-impact.sh setup --mode isvc|all [--namespace <ns>]
#   ./test-upgrade-service-impact.sh start-load
#   ./test-upgrade-service-impact.sh stop-load
#   ./test-upgrade-service-impact.sh report [--before <snap> --after <snap>]
#   ./test-upgrade-service-impact.sh cleanup

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_NS="kserve-upgrade-test"
OUTPUT_DIR="${UPGRADE_TEST_OUTPUT_DIR:-}"
LOAD_GEN_SCRIPT="${SCRIPT_DIR}/load-generator.sh"
REPORT_SCRIPT="${SCRIPT_DIR}/generate-report.sh"
UPGRADE_DIFF="${SCRIPT_DIR}/upgrade-diff.sh"
MANIFESTS_DIR="${SCRIPT_DIR}/test-manifests"

# Headless service doesn't do port mapping — use container port directly
ISVC_SVC_PORT=8080
ISVC_HEALTH_PATH="/v2/health/ready"
LLMISVC_SVC_PORT=8000
LLMISVC_HEALTH_PATH="/v1/models"

usage() {
  cat <<EOF
Usage:
  $0 setup --mode isvc|all [--namespace <ns>] [--output-dir <dir>]
  $0 start-load
  $0 stop-load
  $0 report [--before <snap> --after <snap>]
  $0 cleanup

Commands:
  setup       Deploy test ISVC (and optionally LLMISVC) with ServingRuntime
  start-load  Start load generator pods (runs in cluster, logs collected locally)
  stop-load   Stop load generator pods and collect final logs
  report      Generate HTML report from snapshots + load test logs
  cleanup     Remove all test resources

Modes:
  isvc   Deploy sklearn-iris ISVC only (lightweight, CPU only)
  all    Deploy both ISVC and LLMISVC (requires more resources)

Options:
  --output-dir <dir>   Directory for all test data (snapshots, logs, report).
                       Default: /tmp/upgrade-test-YYYYMMDD-HHMMSS
                       Can also be set via UPGRADE_TEST_OUTPUT_DIR env var.
EOF
  exit 1
}

resolve_output_dir() {
  if [[ -n "$OUTPUT_DIR" ]]; then
    return
  fi
  local state_file="${SCRIPT_DIR}/.current-output-dir"
  if [[ -f "$state_file" ]]; then
    OUTPUT_DIR=$(cat "$state_file")
    return
  fi
  # First run — generate a new directory
  OUTPUT_DIR="/tmp/upgrade-test-$(date +%Y%m%d-%H%M%S)"
}

ensure_output_dir() {
  resolve_output_dir
  mkdir -p "$OUTPUT_DIR"
  echo "$OUTPUT_DIR" > "${SCRIPT_DIR}/.current-output-dir"
  log "Output directory: $OUTPUT_DIR"
}

get_ns() {
  resolve_output_dir
  if [[ -f "$OUTPUT_DIR/namespace" ]]; then
    cat "$OUTPUT_DIR/namespace"
  else
    echo "$DEFAULT_NS"
  fi
}

log() { echo "[$(date +%H:%M:%S)] $*"; }

wait_for_ready() {
  wait_for_condition "$1" "$2" "$3" "Ready" "${4:-300}"
}

wait_for_condition() {
  local kind=$1 name=$2 ns=$3 condition=${4:-Ready} timeout=${5:-300}
  local deadline=$((SECONDS + timeout))
  while [[ $SECONDS -lt $deadline ]]; do
    local status
    status=$(oc get "$kind" "$name" -n "$ns" \
      -o jsonpath="{.status.conditions[?(@.type==\"${condition}\")].status}" 2>/dev/null)
    if [[ "$status" == "True" ]]; then
      return 0
    fi
    sleep 3
  done
  log "ERROR: $kind/$name condition $condition did not become True within ${timeout}s"
  return 1
}

# --- Setup ---

deploy_sklearn_runtime() {
  local ns=$1
  log "Deploying mlserver ServingRuntime..."
  if oc get template mlserver-runtime-template -n opendatahub &>/dev/null; then
    log "  Using OpenShift Template: mlserver-runtime-template"
    oc process -n opendatahub mlserver-runtime-template | oc apply -n "$ns" -f -
  else
    log "  Template not found, using fallback manifest: test-manifests/mlserver-runtime.yaml"
    oc apply -n "$ns" -f "${MANIFESTS_DIR}/mlserver-runtime.yaml"
  fi
  log "ServingRuntime mlserver-runtime deployed"
}

deploy_sklearn_isvc() {
  local ns=$1
  log "Deploying sklearn-iris InferenceService..."
  oc apply -n "$ns" -f "${MANIFESTS_DIR}/sklearn-iris-isvc.yaml"
  log "Waiting for sklearn-iris to be Ready..."
  wait_for_ready "inferenceservice" "sklearn-iris" "$ns" 300
  log "sklearn-iris is Ready"
}

deploy_llmisvc() {
  local ns=$1
  log "Deploying facebook-opt-125m-single LLMInferenceService..."
  oc apply -n "$ns" -f "${MANIFESTS_DIR}/llmisvc-opt-125m-cpu.yaml"
  log "Waiting for facebook-opt-125m-single workloads to be Ready (CPU mode, may take several minutes)..."
  wait_for_condition "llminferenceservice" "facebook-opt-125m-single" "$ns" "WorkloadsReady" 600
  log "facebook-opt-125m-single workloads are Ready"
}

ensure_curl_pod() {
  local ns=$1
  if oc get pod curl-util -n "$ns" &>/dev/null; then
    return 0
  fi
  log "Creating curl utility pod..."
  cat <<EOF | oc apply -n "$ns" -f -
apiVersion: v1
kind: Pod
metadata:
  name: curl-util
  labels:
    app: curl-util
spec:
  containers:
  - name: curl
    image: curlimages/curl:latest
    command: ["sleep", "infinity"]
  restartPolicy: Never
EOF
  oc wait --for=condition=Ready pod/curl-util -n "$ns" --timeout=60s
}

test_endpoint() {
  local ns=$1 svc_name=$2 port=$3 url_path=$4 scheme=${5:-http}
  local url="${scheme}://${svc_name}.${ns}.svc:${port}${url_path}"
  local max_retries=10
  local retry_interval=5
  local curl_opts="-s -o /dev/null -w %{http_code} --connect-timeout 5 --max-time 10"
  [[ "$scheme" == "https" ]] && curl_opts="-sk -o /dev/null -w %{http_code} --connect-timeout 5 --max-time 10"

  ensure_curl_pod "$ns"

  log "Testing endpoint: $url (up to ${max_retries} retries)"
  for i in $(seq 1 "$max_retries"); do
    local http_code
    http_code=$(oc exec curl-util -n "$ns" -- \
      curl $curl_opts "$url" 2>/dev/null) || http_code=0

    if [[ "$http_code" -ge 200 && "$http_code" -lt 300 ]]; then
      log "Endpoint test PASSED (HTTP $http_code) on attempt $i"
      return 0
    fi
    log "  attempt $i/$max_retries: HTTP $http_code, retrying in ${retry_interval}s..."
    sleep "$retry_interval"
  done

  log "Endpoint test FAILED after $max_retries attempts"
  return 1
}

do_setup() {
  local mode="" ns="$DEFAULT_NS"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --mode) mode="$2"; shift 2 ;;
      --namespace) ns="$2"; shift 2 ;;
      --output-dir) OUTPUT_DIR="$2"; shift 2 ;;
      *) echo "Unknown option: $1"; usage ;;
    esac
  done
  [[ -z "$mode" ]] && { echo "Error: --mode is required (isvc|all)"; usage; }

  ensure_output_dir
  echo "$ns" > "$OUTPUT_DIR/namespace"
  echo "$mode" > "$OUTPUT_DIR/mode"

  log "Setting up test resources in namespace: $ns (mode: $mode)"

  oc get namespace "$ns" &>/dev/null || oc create namespace "$ns"

  deploy_sklearn_runtime "$ns"
  deploy_sklearn_isvc "$ns"
  test_endpoint "$ns" "sklearn-iris-predictor" "$ISVC_SVC_PORT" "$ISVC_HEALTH_PATH"

  if [[ "$mode" == "all" ]]; then
    deploy_llmisvc "$ns"
    test_endpoint "$ns" "facebook-opt-125m-single-kserve-workload-svc" "$LLMISVC_SVC_PORT" "$LLMISVC_HEALTH_PATH" "https"
  fi

  log ""
  log "Setup complete. Next steps:"
  log "  1. ./test-upgrade-service-impact.sh start-load"
  log "  2. ./upgrade-diff.sh snapshot --name pre-upgrade"
  log "  3. (perform upgrade)"
  log "  4. ./upgrade-diff.sh snapshot --name post-upgrade"
  log "  5. ./test-upgrade-service-impact.sh stop-load"
  log "  6. ./test-upgrade-service-impact.sh report"
}

# --- Load Generator ---

create_load_generator_pod() {
  local ns=$1 name=$2 url=$3 label=$4

  log "Creating load generator pod: $name"

  oc delete configmap "load-gen-${name}" -n "$ns" --ignore-not-found &>/dev/null
  oc create configmap "load-gen-${name}" -n "$ns" --from-file=load-generator.sh="$LOAD_GEN_SCRIPT"

  cat <<EOF | oc apply -n "$ns" -f -
apiVersion: v1
kind: Pod
metadata:
  name: load-gen-${name}
  labels:
    app: load-generator
    target: ${name}
spec:
  containers:
  - name: generator
    image: curlimages/curl:latest
    command: ["/bin/sh", "/scripts/load-generator.sh"]
    args: ["${url}", "${label}"]
    volumeMounts:
    - name: script
      mountPath: /scripts
  volumes:
  - name: script
    configMap:
      name: load-gen-${name}
      defaultMode: 0755
  restartPolicy: Never
EOF

  oc wait --for=condition=Ready pod/load-gen-${name} -n "$ns" --timeout=60s
  log "Load generator pod load-gen-${name} running"
}

do_start_load() {
  ensure_output_dir
  local ns
  ns=$(get_ns)
  local mode
  mode=$(cat "$OUTPUT_DIR/mode" 2>/dev/null || echo "isvc")

  log "Starting load generators in namespace: $ns (mode: $mode)"

  create_load_generator_pod "$ns" "sklearn-iris" \
    "http://sklearn-iris-predictor.${ns}.svc:${ISVC_SVC_PORT}${ISVC_HEALTH_PATH}" \
    "sklearn-iris"

  if [[ "$mode" == "all" ]]; then
    create_load_generator_pod "$ns" "llmisvc" \
      "https://facebook-opt-125m-single-kserve-workload-svc.${ns}.svc:${LLMISVC_SVC_PORT}${LLMISVC_HEALTH_PATH}" \
      "llmisvc"
  fi

  # Start background log collection
  log "Starting log collection..."
  oc logs -f "load-gen-sklearn-iris" -n "$ns" > "$OUTPUT_DIR/load-test-sklearn-iris.jsonl" 2>/dev/null &
  echo $! > "$OUTPUT_DIR/log-pid-sklearn-iris"
  log "  sklearn-iris log PID: $(cat "$OUTPUT_DIR/log-pid-sklearn-iris")"

  if [[ "$mode" == "all" ]]; then
    oc logs -f "load-gen-llmisvc" -n "$ns" > "$OUTPUT_DIR/load-test-llmisvc.jsonl" 2>/dev/null &
    echo $! > "$OUTPUT_DIR/log-pid-llmisvc"
    log "  llmisvc log PID: $(cat "$OUTPUT_DIR/log-pid-llmisvc")"
  fi

  log ""
  log "Load generators running. Logs being collected to $OUTPUT_DIR/"
  log "Run your upgrade, then: ./test-upgrade-service-impact.sh stop-load"
}

do_stop_load() {
  ensure_output_dir
  local ns
  ns=$(get_ns)
  local mode
  mode=$(cat "$OUTPUT_DIR/mode" 2>/dev/null || echo "isvc")

  log "Stopping load generators..."

  # Kill log collection processes
  for pidfile in "$OUTPUT_DIR"/log-pid-*; do
    [[ -f "$pidfile" ]] || continue
    local pid
    pid=$(cat "$pidfile")
    kill "$pid" 2>/dev/null || true
    rm -f "$pidfile"
  done

  # Delete load generator pods
  oc delete pod -n "$ns" -l app=load-generator --ignore-not-found

  # Print summary
  for logfile in "$OUTPUT_DIR"/load-test-*.jsonl; do
    [[ -f "$logfile" ]] || continue
    local name
    name=$(basename "$logfile" .jsonl | sed 's/load-test-//')
    local total ok fail
    total=$(wc -l < "$logfile")
    ok=$(grep -c '"ok":true' "$logfile" || echo 0)
    fail=$(grep -c '"ok":false' "$logfile" || echo 0)
    log "  $name: $total requests, $ok ok, $fail failed"
  done

  log "Load generators stopped. Logs saved in $OUTPUT_DIR/"
}

# --- Report ---

do_report() {
  local before="pre-upgrade" after="post-upgrade"
  while [[ $# -gt 0 ]]; do
    case $1 in
      --before) before="$2"; shift 2 ;;
      --after) after="$2"; shift 2 ;;
      *) echo "Unknown option: $1"; usage ;;
    esac
  done

  ensure_output_dir

  # Generate diff text
  log "Generating snapshot diff..."
  "$UPGRADE_DIFF" diff --before "$before" --after "$after" > "$OUTPUT_DIR/diff-report.txt" 2>&1

  # Generate HTML report
  log "Generating HTML report..."
  "$REPORT_SCRIPT" \
    --diff "$OUTPUT_DIR/diff-report.txt" \
    --snapshots "$OUTPUT_DIR/snapshots" \
    --before "$before" \
    --after "$after" \
    --load-dir "$OUTPUT_DIR" \
    --output "$OUTPUT_DIR/upgrade-report.html"

  log "Report saved: $OUTPUT_DIR/upgrade-report.html"
}

# --- Cleanup ---

do_cleanup() {
  local ns
  ns=$(get_ns)
  log "Cleaning up test resources in namespace: $ns"

  # Stop any running load generators and utility pods
  oc delete pod -n "$ns" -l app=load-generator --ignore-not-found 2>/dev/null
  oc delete pod -n "$ns" curl-util --ignore-not-found 2>/dev/null
  oc delete configmap -n "$ns" -l app=load-generator --ignore-not-found 2>/dev/null

  # Delete ISVCs and runtimes
  oc delete inferenceservice --all -n "$ns" --ignore-not-found 2>/dev/null
  oc delete servingruntime --all -n "$ns" --ignore-not-found 2>/dev/null

  # Kill log processes
  for pidfile in "$OUTPUT_DIR"/log-pid-*; do
    [[ -f "$pidfile" ]] || continue
    kill "$(cat "$pidfile")" 2>/dev/null || true
    rm -f "$pidfile"
  done

  # Reset output dir tracking so next setup creates a fresh directory
  rm -f "${SCRIPT_DIR}/.current-output-dir"

  log "Cleanup complete (namespace $ns preserved, output dir: $OUTPUT_DIR)"
}

# --- Main ---

[[ $# -eq 0 ]] && usage

case $1 in
  setup) shift; do_setup "$@" ;;
  start-load) shift; do_start_load "$@" ;;
  stop-load) shift; do_stop_load "$@" ;;
  report) shift; do_report "$@" ;;
  cleanup) shift; do_cleanup "$@" ;;
  *) usage ;;
esac
