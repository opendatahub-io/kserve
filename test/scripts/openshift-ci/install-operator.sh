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
#
# In both modes the Subscription always uses installPlanApproval: Manual; the
# script explicitly approves the generated InstallPlan so that upgrades are
# controlled and auditable.
#
# When OPERATOR_VERSION is set the script uses "dev mode":
#   - cleans up any previous subscription/CSV
#   - pins the version via startingCSV
# When OPERATOR_VERSION is empty the script uses "CI mode":
#   - skips install if operator is already running
#   - omits startingCSV (OLM picks the latest in the channel)

set -euo pipefail

_INSTALL_OPERATOR_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${_INSTALL_OPERATOR_DIR}/common.sh"

: "${OPERATOR_TYPE:=odh}"
: "${OPERATOR_VERSION:=}"
: "${CATALOG_SOURCE:=}"
: "${OPERATOR_NAMESPACE:=openshift-operators}"

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
            echo "  OPERATOR_TYPE=odh OPERATOR_VERSION=3.4.0-ea.1 CATALOG_SOURCE=quay.io/opendatahub/opendatahub-operator-catalog:v3.4.0-ea.1 $0"
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

    # HyperShift / ROSA HCP clusters have no MachineConfigPools -- the
    # ImageDigestMirrorSet is applied directly to nodes without an MCP rollout.
    if ! oc get mcp master &>/dev/null; then
        echo "No MachineConfigPool found (HyperShift/ROSA HCP) -- skipping MCP wait"
        return
    fi

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

query_catalog_info() {
    local catalog="$1"
    local csv_pattern="${2:-}"
    oc get packagemanifest --all-namespaces -o json 2>/dev/null | python3 -c "
import json, sys
catalog, csv_pat = '${catalog}', '${csv_pattern}'
data = json.load(sys.stdin)
for item in data.get('items', []):
    st = item.get('status', {})
    if item['metadata']['name'] != '${OPERATOR_NAME}' or st.get('catalogSource') != catalog:
        continue
    channels = st.get('channels', [])
    default_ch = st.get('defaultChannel', '')
    if csv_pat:
        for ch in channels:
            if csv_pat in ch.get('currentCSV', ''):
                print(ch['name'], ch['currentCSV'])
                sys.exit(0)
        for ch in channels:
            for entry in ch.get('entries', []):
                if csv_pat in entry.get('name', ''):
                    print(ch['name'], entry['name'])
                    sys.exit(0)
    if default_ch:
        for ch in channels:
            if ch['name'] == default_ch:
                print(default_ch, ch.get('currentCSV', ''))
                sys.exit(0)
    if channels:
        ch = channels[0]
        print(ch['name'], ch.get('currentCSV', ''))
    break
" 2>/dev/null
}

USE_STARTING_CSV=true

detect_channel() {
    local csv_pattern=""
    if [[ -n "${OPERATOR_VERSION}" ]]; then
        csv_pattern="${OPERATOR_NAME}.${CSV_VERSION}"
    elif [[ "${CATALOG_SOURCE}" == */* && "${CATALOG_SOURCE}" == *:* ]]; then
        local tag="${CATALOG_SOURCE##*:}"
        local version_hint="${tag#rhoai-}"
        version_hint="${version_hint#odh-}"
        local major_minor
        major_minor=$(echo "${version_hint}" | grep -oE '^[0-9]+\.[0-9]+' || true)
        if [[ -n "${major_minor}" ]]; then
            csv_pattern="${major_minor}"
            echo "  Inferred version hint '${csv_pattern}' from CATALOG_SOURCE tag '${tag}'"
        fi
    fi

    echo "Querying catalog '${OPERATOR_SOURCE}' for channel (csv_pattern='${csv_pattern:-any}')..."
    local info
    info=$(query_catalog_info "${OPERATOR_SOURCE}" "${csv_pattern}" || true)
    if [[ -z "${info}" ]]; then
        echo "  Could not query catalog; using fallback channel: ${OPERATOR_CHANNEL}"
        return
    fi

    local detected_channel detected_csv
    detected_channel=$(echo "${info}" | awk '{print $1}')
    detected_csv=$(echo "${info}" | awk '{print $2}')
    echo "  Catalog reports: channel=${detected_channel} csv=${detected_csv}"
    OPERATOR_CHANNEL="${detected_channel}"

    if [[ -n "${OPERATOR_VERSION}" && -n "${csv_pattern}" && "${detected_csv}" != "${csv_pattern}" ]]; then
        echo "  ERROR: requested ${csv_pattern} but catalog only offers ${detected_csv}"
        echo "  Check that OPERATOR_VERSION=${OPERATOR_VERSION} is available in the catalog."
        exit 1
    elif [[ -z "${OPERATOR_VERSION}" && -n "${csv_pattern}" && -n "${detected_csv}" ]]; then
        CSV_VERSION="${detected_csv#${OPERATOR_NAME}.}"
        OPERATOR_VERSION="${CSV_VERSION#v}"
        USE_STARTING_CSV=false
        echo "  Auto-detected version: ${OPERATOR_VERSION} (csv=${detected_csv})"
    fi
}

cleanup_previous_install() {
    local existing_sub
    existing_sub=$(oc get subscription "${OPERATOR_NAME}" -n "${OPERATOR_NAMESPACE}" -o name 2>/dev/null || echo "")
    if [[ -n "${existing_sub}" ]]; then
        echo "Cleaning up previous installation..."
        oc delete subscription "${OPERATOR_NAME}" -n "${OPERATOR_NAMESPACE}" --ignore-not-found 2>/dev/null
        oc delete csv -n "${OPERATOR_NAMESPACE}" -l "operators.coreos.com/${OPERATOR_NAME}.${OPERATOR_NAMESPACE}" --ignore-not-found 2>/dev/null
    fi

    local failed_jobs
    failed_jobs=$(oc get jobs -n openshift-marketplace --no-headers 2>/dev/null \
        | awk '$3 == "Failed" || $2 == "0/1" {print $1}' | head -5 || true)
    if [[ -n "${failed_jobs}" ]]; then
        echo "Removing stale unpack jobs in openshift-marketplace..."
        echo "${failed_jobs}" | xargs -r oc delete job -n openshift-marketplace --ignore-not-found 2>/dev/null
    fi
}

# Returns 0 (true) if operator is already installed and running.
check_already_installed() {
    local existing_csv
    existing_csv=$(oc get subscription "${OPERATOR_NAME}" -n "${OPERATOR_NAMESPACE}" \
        -o=jsonpath='{.status.installedCSV}' 2>/dev/null || true)
    if [[ -n "${existing_csv}" ]]; then
        local csv_status
        csv_status=$(oc get csv "${existing_csv}" -n "${OPERATOR_NAMESPACE}" \
            -o=jsonpath='{.status.phase}' 2>/dev/null || true)
        if [[ "${csv_status}" == "Succeeded" ]]; then
            echo "${OPERATOR_NAME} already installed and ready (${existing_csv}), skipping installation"
            return 0
        fi
    fi
    return 1
}

apply_subscription() {
    local starting_csv_line=""

    if [[ -n "${OPERATOR_VERSION}" ]]; then
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
  namespace: ${OPERATOR_NAMESPACE}
spec:
  channel: ${OPERATOR_CHANNEL}
  name: ${OPERATOR_NAME}
  source: ${OPERATOR_SOURCE}
  sourceNamespace: openshift-marketplace
  installPlanApproval: Manual
${starting_csv_line}
EOF
}

wait_for_operator_ready() {
    echo "Waiting for install plan to be created..."
    timeout 300 bash -c "
        while true; do
            install_plan=\$(oc get subscription ${OPERATOR_NAME} -n ${OPERATOR_NAMESPACE} -o jsonpath=\"{.status.installPlanRef.name}\" 2>/dev/null || echo \"\")
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
    install_plan=$(oc get subscription "${OPERATOR_NAME}" -n "${OPERATOR_NAMESPACE}" -o jsonpath="{.status.installPlanRef.name}")
    oc patch installplan "${install_plan}" -n "${OPERATOR_NAMESPACE}" --type merge -p '{"spec":{"approved":true}}'
    echo "Install plan approved"

    echo "Waiting for ${OPERATOR_NAME} CSV to succeed..."
    timeout 300 bash -c "
        while true; do
            phase=\$(oc get csv -n ${OPERATOR_NAMESPACE} -l operators.coreos.com/${OPERATOR_NAME}.${OPERATOR_NAMESPACE} -o jsonpath=\"{.items[0].status.phase}\" 2>/dev/null || echo \"Pending\")
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
        while ! oc get deployment ${CONTROLLER_DEPLOYMENT} -n "${OPERATOR_NAMESPACE}" &>/dev/null; do
            echo \"  Waiting for ${CONTROLLER_DEPLOYMENT} deployment to be created...\"
            sleep 10
        done
        oc wait deployment/${CONTROLLER_DEPLOYMENT} -n "${OPERATOR_NAMESPACE}" \
            --for=condition=Available \
            --timeout=300s
    "
    echo "${CONTROLLER_DEPLOYMENT} is available"

    echo "Waiting for ODH/RHOAI CRDs to be established..."
    wait_for_crd "dscinitializations.dscinitialization.opendatahub.io" 90s
    wait_for_crd "datascienceclusters.datasciencecluster.opendatahub.io" 90s
}

install_operator() {
    resolve_operator_vars || return 1
    resolve_catalog_source
    ensure_image_mirror

    local version_before="${OPERATOR_VERSION}"
    if [[ -n "${OPERATOR_VERSION}" ]]; then
        cleanup_previous_install
    else
        if check_already_installed; then
            return 0
        fi
    fi

    detect_channel

    if [[ -z "${version_before}" && -n "${OPERATOR_VERSION}" ]]; then
        cleanup_previous_install
    fi

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
    install_operator
fi
