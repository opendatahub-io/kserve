#!/usr/bin/env bash
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Installs an ODH or RHOAI operator via OLM Subscription.
#
# Can be called directly (runs the full install sequence) or sourced by
# another script that wants to call individual functions.
#
# Env-var interface (all optional):
#   OPERATOR_TYPE      odh (default) | rhods/rhoai
#   OPERATOR_VERSION   e.g. 3.4.0; empty = latest in channel (CI default)
#   CATALOG_SOURCE     FBC fragment image, CatalogSource name, or empty (default catalog)
#   MIRROR_IMAGES      true | false (default); creates ImageDigestMirrorSet
#   CHANNEL_OVERRIDE   explicit OLM channel; empty = auto-detect
#
# When OPERATOR_VERSION is set the script uses "dev mode":
#   - cleans up any previous subscription/CSV
#   - pins the version via startingCSV + Manual install-plan approval
# When OPERATOR_VERSION is empty the script uses "CI mode":
#   - skips install if operator is already running
#   - uses Automatic install-plan approval, no startingCSV

_INSTALL_OPERATOR_SOURCED=true

INSTALL_OPERATOR_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

: "${OPERATOR_TYPE:=odh}"
: "${OPERATOR_VERSION:=}"
: "${CATALOG_SOURCE:=}"
: "${CHANNEL_OVERRIDE:=}"

# Auto-enable image mirroring for RHOAI with FBC fragment images
if [[ -z "${MIRROR_IMAGES:-}" ]]; then
    if [[ "${OPERATOR_TYPE}" =~ ^(rhods|rhoai)$ ]] && [[ "${CATALOG_SOURCE}" == */* ]]; then
        MIRROR_IMAGES=true
    else
        MIRROR_IMAGES=false
    fi
fi

resolve_operator_vars() {
    case "${OPERATOR_TYPE}" in
        odh|opendatahub)
            OPERATOR_NAME="opendatahub-operator"
            DEFAULT_SOURCE="community-operators"
            CONTROLLER_DEPLOYMENT="opendatahub-operator-controller-manager"
            if [[ -n "${OPERATOR_VERSION}" ]]; then
                CSV_VERSION="v${OPERATOR_VERSION}"
            fi
            if [[ "${OPERATOR_VERSION}" == 3.* ]] || [[ -z "${OPERATOR_VERSION}" ]]; then
                OPERATOR_CHANNEL="fast-3"
            else
                OPERATOR_CHANNEL="fast"
            fi
            ;;
        rhods|rhoai)
            OPERATOR_NAME="rhods-operator"
            DEFAULT_SOURCE="redhat-operators"
            CONTROLLER_DEPLOYMENT="rhods-operator"
            if [[ -n "${OPERATOR_VERSION}" ]]; then
                CSV_VERSION="${OPERATOR_VERSION}"
            fi
            if [[ "${OPERATOR_VERSION}" == 3.* ]] || [[ -z "${OPERATOR_VERSION}" ]]; then
                OPERATOR_CHANNEL="fast-3.x"
            else
                OPERATOR_CHANNEL="fast"
            fi
            ;;
        *)
            echo "Error: Unknown operator type '${OPERATOR_TYPE}'"
            echo "Env vars: OPERATOR_TYPE=odh|rhods  OPERATOR_VERSION=3.4.0  CATALOG_SOURCE=<image>"
            echo ""
            echo "Examples:"
            echo "  OPERATOR_TYPE=rhods OPERATOR_VERSION=3.4.0 CATALOG_SOURCE=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.4 $0"
            echo "  OPERATOR_TYPE=odh OPERATOR_VERSION=3.2.0 $0"
            echo "  OPERATOR_TYPE=odh $0   # latest ODH in fast-3 channel (CI mode)"
            return 1
            ;;
    esac
}

resolve_catalog_source() {
    if [[ -z "${CATALOG_SOURCE}" ]]; then
        OPERATOR_SOURCE="${DEFAULT_SOURCE}"
        return
    fi
    if [[ "${CATALOG_SOURCE}" == */* ]]; then
        local cs_name="${OPERATOR_NAME}-custom-catalog"
        echo "Creating CatalogSource '${cs_name}' from image: ${CATALOG_SOURCE}"
        cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: ${cs_name}
  namespace: openshift-marketplace
spec:
  displayName: "${OPERATOR_NAME} (custom)"
  image: ${CATALOG_SOURCE}
  publisher: Custom
  sourceType: grpc
  updateStrategy:
    registryPoll:
      interval: 30m
EOF
        echo "Waiting for CatalogSource to become ready..."
        timeout 120 bash -c "
            while true; do
                state=\$(oc get catalogsource ${cs_name} -n openshift-marketplace -o jsonpath='{.status.connectionState.lastObservedState}' 2>/dev/null || echo '')
                if [[ \"\${state}\" == 'READY' ]]; then
                    echo '  CatalogSource is READY'
                    break
                fi
                echo \"  CatalogSource state: \${state:-pending}\"
                sleep 5
            done
        "
        OPERATOR_SOURCE="${cs_name}"
    else
        OPERATOR_SOURCE="${CATALOG_SOURCE}"
    fi
}

ensure_image_mirror() {
    [[ "${MIRROR_IMAGES}" == "true" ]] || return 0
    [[ "${CATALOG_SOURCE}" == */* ]] || return 0

    local image_path="${CATALOG_SOURCE%%:*}"
    local image_org="${image_path%/*}"
    local org_name="${image_org##*/}"
    local idms_name="${org_name}-quay-mirror"

    if oc get imagedigestmirrorset "${idms_name}" &>/dev/null; then
        echo "ImageDigestMirrorSet '${idms_name}' already exists, skipping"
        return
    fi

    echo "Creating ImageDigestMirrorSet '${idms_name}': registry.redhat.io/${org_name} -> ${image_org}"
    cat <<EOF | oc apply -f -
apiVersion: config.openshift.io/v1
kind: ImageDigestMirrorSet
metadata:
  name: ${idms_name}
spec:
  imageDigestMirrors:
  - source: registry.redhat.io/${org_name}
    mirrors:
    - ${image_org}
EOF

    echo "Waiting for MachineConfigPool update (this takes 1-2 min on CRC)..."
    sleep 5
    timeout 300 bash -c "
        while true; do
            updating=\$(oc get mcp master -o jsonpath='{.status.conditions[?(@.type==\"Updating\")].status}' 2>/dev/null || echo 'Unknown')
            updated=\$(oc get mcp master -o jsonpath='{.status.conditions[?(@.type==\"Updated\")].status}' 2>/dev/null || echo 'Unknown')
            echo \"  MCP: updating=\${updating} updated=\${updated}\"
            if [[ \"\${updated}\" == 'True' && \"\${updating}\" == 'False' ]]; then
                echo '  MachineConfigPool update complete'
                break
            fi
            sleep 10
        done
    "
}

parse_major_minor() {
    local ver="$1"
    ver="${ver%%-*}"
    echo "${ver%.*}"
}

is_ea_version() {
    [[ "${OPERATOR_VERSION}" == *-ea* || "${OPERATOR_VERSION}" == *-ea.* ]]
}

query_catalog_channels() {
    local catalog="$1"
    oc get packagemanifest --all-namespaces -o json 2>/dev/null | python3 -c "
import json, sys
data = json.load(sys.stdin)
for item in data.get('items', []):
    if item['metadata']['name'] == '${OPERATOR_NAME}' and item['status']['catalogSource'] == '${catalog}':
        print(' '.join(ch['name'] for ch in item['status']['channels']))
        break
" 2>/dev/null
}

query_current_csv() {
    local catalog="$1"
    local channel="$2"
    oc get packagemanifest --all-namespaces -o json 2>/dev/null | python3 -c "
import json, sys
data = json.load(sys.stdin)
for item in data.get('items', []):
    if item['metadata']['name'] == '${OPERATOR_NAME}' and item['status'].get('catalogSource') == '${catalog}':
        for ch in item['status'].get('channels', []):
            if ch['name'] == '${channel}':
                print(ch.get('currentCSV', ''))
                break
        break
" 2>/dev/null
}

detect_channel() {
    if [[ -n "${CHANNEL_OVERRIDE}" ]]; then
        OPERATOR_CHANNEL="${CHANNEL_OVERRIDE}"
        echo "Using channel override: ${OPERATOR_CHANNEL}"
        return
    fi
    if [[ -z "${OPERATOR_VERSION}" ]]; then
        echo "Using default channel: ${OPERATOR_CHANNEL}"
        return
    fi

    echo "Detecting available channels from CatalogSource '${OPERATOR_SOURCE}'..."
    local channels
    channels=$(query_catalog_channels "${OPERATOR_SOURCE}")
    if [[ -z "${channels}" ]]; then
        echo "  Could not query channels from '${OPERATOR_SOURCE}'; using default: ${OPERATOR_CHANNEL}"
        return
    fi
    echo "  Available channels: ${channels}"

    local major_minor
    major_minor=$(parse_major_minor "${OPERATOR_VERSION}")

    local candidates=()
    if is_ea_version; then
        candidates=("beta" "stable-${major_minor}" "fast-${major_minor}" "${OPERATOR_CHANNEL}" "fast")
    else
        candidates=("stable-${major_minor}" "stable-${OPERATOR_VERSION}" "fast-${major_minor}" "${OPERATOR_CHANNEL}" "fast")
    fi

    for candidate in "${candidates[@]}"; do
        if echo "${channels}" | tr ' ' '\n' | grep -qx "${candidate}"; then
            OPERATOR_CHANNEL="${candidate}"
            echo "  Selected channel: ${OPERATOR_CHANNEL}"
            return
        fi
    done
    echo "  No preferred channel matched; using default: ${OPERATOR_CHANNEL}"
}

USE_STARTING_CSV=true

validate_csv() {
    [[ -n "${OPERATOR_VERSION}" ]] || return 0

    local requested_csv="${OPERATOR_NAME}.${CSV_VERSION}"
    echo "Validating ${requested_csv} in channel ${OPERATOR_CHANNEL}..."
    local current_csv
    current_csv=$(query_current_csv "${OPERATOR_SOURCE}" "${OPERATOR_CHANNEL}")
    if [[ -z "${current_csv}" ]]; then
        echo "  Could not query catalog; proceeding with requested version"
        return
    fi
    if [[ "${current_csv}" == "${requested_csv}" ]]; then
        echo "  Confirmed: ${requested_csv} is current in channel"
        return
    fi
    echo "  WARNING: ${requested_csv} not found as current CSV in channel ${OPERATOR_CHANNEL}"
    echo "  Latest available: ${current_csv}"
    CSV_VERSION="${current_csv#${OPERATOR_NAME}.}"
    OPERATOR_VERSION="${CSV_VERSION#v}"
    USE_STARTING_CSV=false
    echo "  Falling back to ${current_csv} (omitting startingCSV to let OLM resolve)"
}

cleanup_previous_install() {
    local existing_sub
    existing_sub=$(oc get subscription "${OPERATOR_NAME}" -n openshift-operators -o name 2>/dev/null || echo "")
    if [[ -n "${existing_sub}" ]]; then
        echo "Cleaning up previous installation..."
        oc delete subscription "${OPERATOR_NAME}" -n openshift-operators --ignore-not-found 2>/dev/null
        oc delete csv -n openshift-operators -l "operators.coreos.com/${OPERATOR_NAME}.openshift-operators" --ignore-not-found 2>/dev/null
    fi

    local failed_jobs
    failed_jobs=$(oc get jobs -n openshift-marketplace --no-headers 2>/dev/null \
        | awk '$3 == "Failed" || $2 == "0/1" {print $1}' | head -5)
    if [[ -n "${failed_jobs}" ]]; then
        echo "Removing stale unpack jobs in openshift-marketplace..."
        echo "${failed_jobs}" | xargs -r oc delete job -n openshift-marketplace --ignore-not-found 2>/dev/null
    fi
}

# Returns 0 (true) if operator is already installed and running.
check_already_installed() {
    local existing_csv
    existing_csv=$(oc get subscription "${OPERATOR_NAME}" -n openshift-operators \
        -o=jsonpath='{.status.installedCSV}' 2>/dev/null || true)
    if [[ -n "${existing_csv}" ]]; then
        local csv_status
        csv_status=$(oc get csv "${existing_csv}" -n openshift-operators \
            -o=jsonpath='{.status.phase}' 2>/dev/null || true)
        if [[ "${csv_status}" == "Succeeded" ]]; then
            echo "${OPERATOR_NAME} already installed and ready (${existing_csv}), skipping installation"
            return 0
        fi
    fi
    return 1
}

apply_subscription() {
    local install_plan_approval="Automatic"
    local starting_csv_line=""

    if [[ -n "${OPERATOR_VERSION}" ]]; then
        install_plan_approval="Manual"
        if [[ "${USE_STARTING_CSV}" == "true" ]]; then
            starting_csv_line="  startingCSV: ${OPERATOR_NAME}.${CSV_VERSION}"
        else
            echo "  (omitting startingCSV -- OLM will pick latest in channel)"
        fi
    fi

    cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: ${OPERATOR_NAME}
  namespace: openshift-operators
spec:
  channel: ${OPERATOR_CHANNEL}
  name: ${OPERATOR_NAME}
  source: ${OPERATOR_SOURCE}
  sourceNamespace: openshift-marketplace
  installPlanApproval: ${install_plan_approval}
${starting_csv_line}
EOF
}

wait_for_operator_ready() {
    if [[ -n "${OPERATOR_VERSION}" ]]; then
        echo "Waiting for install plan to be created..."
        timeout 300 bash -c "
            while true; do
                install_plan=\$(oc get subscription ${OPERATOR_NAME} -n openshift-operators -o jsonpath=\"{.status.installplan.name}\" 2>/dev/null || echo \"\")
                if [[ -n \"\${install_plan}\" ]]; then
                    echo \"  Found install plan: \${install_plan}\"
                    break
                fi
                echo \"  Waiting for install plan...\"
                sleep 5
            done
        "
        echo "Approving install plan..."
        local install_plan
        install_plan=$(oc get subscription "${OPERATOR_NAME}" -n openshift-operators -o jsonpath="{.status.installplan.name}")
        oc patch installplan "${install_plan}" -n openshift-operators --type merge -p '{"spec":{"approved":true}}'
        echo "Install plan approved"
    fi

    echo "Waiting for ${OPERATOR_NAME} CSV to succeed..."
    timeout 300 bash -c "
        while true; do
            phase=\$(oc get csv -n openshift-operators -l operators.coreos.com/${OPERATOR_NAME}.openshift-operators -o jsonpath=\"{.items[0].status.phase}\" 2>/dev/null || echo \"Pending\")
            echo \"  CSV phase: \${phase}\"
            if [[ \"\${phase}\" == \"Succeeded\" ]]; then
                break
            fi
            sleep 10
        done
    "
    echo "${OPERATOR_NAME} installed successfully"

    echo "Waiting for ${CONTROLLER_DEPLOYMENT} deployment to be available..."
    timeout 300 bash -c "
        while ! oc get deployment ${CONTROLLER_DEPLOYMENT} -n openshift-operators &>/dev/null; do
            echo \"  Waiting for ${CONTROLLER_DEPLOYMENT} deployment to be created...\"
            sleep 10
        done
        oc wait deployment/${CONTROLLER_DEPLOYMENT} -n openshift-operators \
            --for=condition=Available \
            --timeout=300s
    "
    echo "${CONTROLLER_DEPLOYMENT} is available"
}

install_operator() {
    resolve_operator_vars || return 1
    resolve_catalog_source
    ensure_image_mirror

    if [[ -n "${OPERATOR_VERSION}" ]]; then
        cleanup_previous_install
    else
        if check_already_installed; then
            return 0
        fi
    fi

    detect_channel
    validate_csv

    local version_display="${OPERATOR_VERSION:-latest}"
    echo "Installing ${OPERATOR_NAME} (${version_display})..."
    echo "  Source:  ${OPERATOR_SOURCE}"
    echo "  Channel: ${OPERATOR_CHANNEL}"

    apply_subscription
    wait_for_operator_ready

    echo "Done! ${OPERATOR_NAME} (${version_display}) installed and ready."
}

# When executed directly (not sourced), run the full install sequence.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    set -euo pipefail
    install_operator
fi
