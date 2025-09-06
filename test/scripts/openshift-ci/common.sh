#!/usr/bin/env bash

set -euo pipefail

# find_project_root [start_dir] [marker]
#   start_dir : directory to begin the search (defaults to this script’s dir)
#   marker    : filename or directory name to look for (defaults to "go.mod")
#
# Prints the first dir containing the marker, or exits 1 if none found.
find_project_root() {
  local start_dir="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
  local marker="${2:-go.mod}"
  local dir="$start_dir"

  while [[ "$dir" != "/" && ! -e "$dir/$marker" ]]; do
    dir="$(dirname "$dir")"
  done

  if [[ -e "$dir/$marker" ]]; then
    printf '%s\n' "$dir"
  else
    echo "Error: couldn’t find '$marker' in any parent of '$start_dir'" >&2
    return 1
  fi
}

readonly VALID_DEPLOYMENT_PROFILES=(raw serverless llm-d)
# validate_deployment_profile [value]
validate_deployment_profile() {
  local profile="$1"
  if [[ ! " ${VALID_DEPLOYMENT_PROFILES[*]} " =~ " ${DEPLOYMENT_PROFILE} " ]]; then
    echo "Error: '$DEPLOYMENT_PROFILE' is not a valid deployment profile." >&2
    echo "Allowed values: ${VALID_DEPLOYMENT_PROFILES[*]}" >&2
    exit 1
  fi
}

# Usage: wait_for_crd <crd-name> [timeout]
#   <crd-name> : the full CRD name (e.g. leaderworkersetoperators.operator.openshift.io)
#   [timeout]  : oc wait timeout (default “60s”)
wait_for_crd() {
  local crd="$1"
  local timeout="${2:-60s}"

  echo "⏳ Waiting for CRD ${crd} to appear (timeout: ${timeout})…"
  if ! timeout "$timeout" bash -c 'until oc get crd "$1" &>/dev/null; do sleep 2; done' _ "$crd"; then
    echo "❌ Timed out after $timeout waiting for CRD $crd to appear." >&2
    return 1
  fi

  echo "⏳ CRD ${crd} detected — waiting for it to become Established (timeout: ${timeout})…"
  oc wait --for=condition=Established --timeout="$timeout" "crd/$crd"
}

# Helper function to wait for a pod with a given label to be created
wait_for_pod_labeled() {
  local ns=${1:?namespace is required}
  local podlabel=${2:?pod label is required}

  echo "Waiting for pod -l \"$podlabel\" in namespace \"$ns\" to be created..."
  until oc get pod -n "$ns" -l "$podlabel" -o=jsonpath='{.items[0].metadata.name}' >/dev/null 2>&1; do
    sleep 2
  done
  echo "Pod -l \"$podlabel\" in namespace \"$ns\" found."
}

# Helper function to wait for a pod with a given label to become ready
wait_for_pod_ready() {
  local ns=${1:?namespace is required}
  local podlabel=${2:?pod label is required}
  local timeout=${3:-600s} # Default timeout 600s

  wait_for_pod_labeled "$ns" "$podlabel"
  sleep 5 # Brief pause to allow K8s to fully register pod status

  echo "Current pods for -l \"$podlabel\" in namespace \"$ns\":"
  oc get pod -n "$ns" -l "$podlabel"

  echo "Waiting up to $timeout for pod(s) -l \"$podlabel\" in namespace \"$ns\" to become ready..."
  if ! oc wait --for=condition=ready --timeout="$timeout" pod -n "$ns" -l "$podlabel"; then
    echo "ERROR: Pod(s) -l \"$podlabel\" in namespace \"$ns\" did not become ready in time."
    echo "Describing pod(s):"
    oc describe pod -n "$ns" -l "$podlabel"

    # Try to get logs from the first pod matching the label if any exist
    local first_pod_name
    first_pod_name=$(oc get pod -n "$ns" -l "$podlabel" -o=jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

    if [ -n "$first_pod_name" ]; then
        echo "Logs for pod \"$first_pod_name\" in namespace \"$ns\" (last 100 lines per container):"
        oc logs -n "$ns" "$first_pod_name" --all-containers --tail=100 || echo "Could not retrieve logs for $first_pod_name."
    else
        echo "No pods found matching -l \"$podlabel\" in namespace \"$ns\" to retrieve logs from."
    fi
    return 1 # Indicate failure
  fi
  echo "Pod(s) -l \"$podlabel\" in namespace \"$ns\" are ready."
}

# get_openshift_server_version
#   Extracts the Server Version from 'oc version' output
#   Returns the version string (e.g., "4.19.9") or exits with error if not found
get_openshift_server_version() {
  local version_output
  local server_version

  # Get oc version output
  if ! version_output=$(oc version 2>/dev/null); then
    echo "Error: Failed to execute 'oc version'. Make sure oc is installed and you're logged in to OpenShift." >&2
    return 1
  fi

  # Extract Server Version line and get the version number
  if server_version=$(echo "$version_output" | grep "Server Version:" | awk '{print $3}'); then
    if [ -n "$server_version" ]; then
      echo "$server_version"
      return 0
    fi
  fi

  echo "Error: Could not find Server Version in 'oc version' output." >&2
  echo "oc version output:" >&2
  echo "$version_output" >&2
  return 1
}

# version_compare <version1> <version2>
#   Compares two version strings in semantic version format (e.g., "4.19.9")
#   Returns 0 if version1 >= version2, 1 otherwise
version_compare() {
  local version1="$1"
  local version2="$2"
  
  # Convert versions to comparable format by padding with zeros
  local v1=$(echo "$version1" | awk -F. '{printf "%d%03d%03d", $1, $2, $3}')
  local v2=$(echo "$version2" | awk -F. '{printf "%d%03d%03d", $1, $2, $3}')
  
  [ "$v1" -ge "$v2" ]
}
