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
# This script installs the ODH/RHOAI operator and fully deploys KServe via DSCI/DSC.
# Optionally copies custom PR manifests to the operator PVC before activating.
# Based on: https://github.com/opendatahub-io/opendatahub-operator/blob/main/hack/component-dev/README.md
#
# Env-var interface:
#   OPERATOR_TYPE        odh (default) | rhoai/rhods
#   CATALOG_SOURCE       FBC fragment image or CatalogSource name
#   COPY_PR_MANIFESTS    true (default) | false -- skip to use bundled operator manifests
#   KSERVE_NAMESPACE     override; derived from OPERATOR_TYPE when not set
#
# NOTE: This is for development/testing only, not for production use

set -eu

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
PROJECT_ROOT="${SCRIPT_DIR}/../../../"
source "${SCRIPT_DIR}/common.sh"
source "${SCRIPT_DIR}/install-operator.sh"

: "${OPERATOR_TYPE:=odh}"
: "${CATALOG_SOURCE:=${ODH_OPERATOR_SOURCE:-}}"

# Derive KSERVE_NAMESPACE from OPERATOR_TYPE when not explicitly set.
case "${OPERATOR_TYPE}" in
  rhods|rhoai)     : "${KSERVE_NAMESPACE:=redhat-ods-applications}" ;;
  odh|opendatahub) : "${KSERVE_NAMESPACE:=opendatahub}" ;;
  *)               : "${KSERVE_NAMESPACE:=kserve}" ;;
esac

echo "Installing ${OPERATOR_TYPE} operator to manage KServe deployment (namespace: ${KSERVE_NAMESPACE})..."

install_operator

: "${COPY_PR_MANIFESTS:=true}"

if [[ "$COPY_PR_MANIFESTS" == "true" ]]; then
  echo "Configuring operator to use custom KServe manifests from PR..."

  echo "Creating PVC for custom KServe manifests..."
  oc apply -f "${SCRIPT_DIR}/odh-operator-custom-manifests/pvc.yaml"
  echo "PVC created (will bind when consumed by operator pod)"

  echo "Patching operator CSV to mount custom manifests volume..."
  CSV=$(oc get subscription "${OPERATOR_NAME}" -n "${OPERATOR_NAMESPACE}" -o jsonpath='{.status.installedCSV}')
  echo "Found CSV: $CSV"

  if oc get csv "$CSV" -n ${OPERATOR_NAMESPACE} -o json | jq -e '.spec.install.spec.deployments[0].spec.template.spec.volumes[] | select(.name=="kserve-custom-manifests")' > /dev/null 2>&1; then
    echo "Volume already mounted, skipping patch"
  else
    echo "Applying CSV patch to mount custom manifests volume..."
    oc patch csv "$CSV" -n ${OPERATOR_NAMESPACE} --type json --patch-file "${SCRIPT_DIR}/odh-operator-custom-manifests/csv-patch.json"
  fi

  OPERATOR_POD_SELECTOR=$(oc get deployment "${CONTROLLER_DEPLOYMENT}" -n "${OPERATOR_NAMESPACE}" \
    -o go-template='{{range $k,$v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}' 2>/dev/null || true)
  OPERATOR_POD_SELECTOR="${OPERATOR_POD_SELECTOR%,}"
  OPERATOR_POD_SELECTOR="${OPERATOR_POD_SELECTOR:-name=${OPERATOR_NAME}}"

  echo "Waiting for operator pod to restart with custom manifests volume (selector: ${OPERATOR_POD_SELECTOR})..."
  oc wait --for='jsonpath={.status.conditions[?(@.type=="Ready")].status}=True' \
    pod -l "${OPERATOR_POD_SELECTOR}" -n ${OPERATOR_NAMESPACE} \
    --timeout=300s 2>/dev/null || true

  sleep 5

  wait_for_pod_ready "${OPERATOR_NAMESPACE}" "${OPERATOR_POD_SELECTOR}" 300s

  echo "Copying PR manifests into ODH operator PVC..."
  "${SCRIPT_DIR}/copy-kserve-manifests-to-pvc.sh"
else
  echo "Vanilla operator install -- using bundled manifests"
fi

# Apply DSC/DSCI to trigger KServe deployment.
# RHOAI auto-creates a default DSCI; wait for it rather than racing to apply our own.
# ODH may not auto-create one, so apply ours with the correct namespace.
if [[ "${OPERATOR_TYPE}" =~ ^(rhods|rhoai)$ ]]; then
  echo "Waiting for RHOAI to auto-create DSCI..."
  timeout 120 bash -c '
    while ! oc get dscinitializations -o name 2>/dev/null | grep -q .; do
      echo "  Waiting for DSCI..."
      sleep 5
    done
  '
  echo "DSCI found: $(oc get dscinitializations -o name)"
elif oc get dscinitializations -o name 2>/dev/null | grep -q .; then
  echo "DSCI already exists, skipping apply"
else
  echo "Applying DSCI..."
  sed 's/applicationsNamespace:  kserve/applicationsNamespace: opendatahub/' \
    "${PROJECT_ROOT}/config/overlays/odh-test/dsci.yaml" | oc apply -f -
fi

echo "Installing KServe CRDs..."
kustomize build "${PROJECT_ROOT}/config/overlays/odh-crds" | oc apply --server-side=true --force-conflicts -f -

echo "Applying DSC to trigger operator deployment..."
oc apply -f "${PROJECT_ROOT}/config/overlays/odh-test/dsc.yaml"

echo "Waiting for operator to deploy KServe controller..."
wait_for_pod_ready "${KSERVE_NAMESPACE}" "control-plane=kserve-controller-manager" 600s

echo "ODH/RHOAI operator deployed KServe successfully"
