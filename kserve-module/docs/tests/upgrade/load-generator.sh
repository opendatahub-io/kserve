#!/bin/bash
# Load generator for service availability checking.
# Sends GET requests back-to-back (next request immediately after response).
# Outputs JSONL to stdout — one line per request.
#
# Usage (inside a pod):
#   ./load-generator.sh <url> [label]
#
# Example:
#   ./load-generator.sh \
#     "http://onnx-mnist-predictor.test.svc:80/v2/health/ready" \
#     onnx-mnist

set -uo pipefail

URL="${1:?Usage: load-generator.sh <url> [label]}"
LABEL="${2:-default}"
TIMEOUT=5

trap 'exit 0' SIGTERM SIGINT

while true; do
  start_ns=$(date +%s%N 2>/dev/null || date +%s)
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || date -Iseconds)

  http_code=$(curl -sk -o /dev/null -w '%{http_code}' \
    --connect-timeout "$TIMEOUT" --max-time "$TIMEOUT" \
    "$URL" 2>/dev/null) || http_code=0

  end_ns=$(date +%s%N 2>/dev/null || date +%s)

  if [[ "$start_ns" == *N ]]; then
    latency_ms=0
  else
    latency_ms=$(( (end_ns - start_ns) / 1000000 ))
  fi

  if [[ "$http_code" -ge 200 && "$http_code" -lt 300 ]]; then
    ok=true
    error=""
  else
    ok=false
    if [[ "$http_code" == "0" ]]; then
      error="connection_failed"
    elif [[ "$http_code" == "000" ]]; then
      error="timeout"
    else
      error="http_${http_code}"
    fi
  fi

  if [[ -z "$error" ]]; then
    printf '{"ts":"%s","label":"%s","status":%s,"latency_ms":%s,"ok":%s}\n' \
      "$ts" "$LABEL" "$http_code" "$latency_ms" "$ok"
  else
    printf '{"ts":"%s","label":"%s","status":%s,"latency_ms":%s,"ok":%s,"error":"%s"}\n' \
      "$ts" "$LABEL" "$http_code" "$latency_ms" "$ok" "$error"
  fi
done
