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
# Applies DSCI and DSC resources to trigger KServe deployment via the operator.
# Optionally installs cert-manager and Kueue operators when ENABLE_KUEUE=true.
#
# Env-var interface:
#   ENABLE_KUEUE   true | false (default)
#   DSCI_FILE      path to DSCI YAML (default: config/overlays/odh-test/dsci.yaml)
#   DSC_FILE       path to DSC YAML  (default: config/overlays/odh-test/dsc.yaml)
#
# The DSC YAML may contain $KUEUE_STATE which is envsubst'd to
# "Unmanaged" (kueue enabled) or "Removed" (kueue disabled).
#
# Kueue/cert-manager subscription YAMLs are expected at .vscode/resources/
# relative to the project root. These are only needed when ENABLE_KUEUE=true.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

: "${ENABLE_KUEUE:=false}"
: "${DSCI_FILE:=${PROJECT_ROOT}/config/overlays/odh-test/dsci.yaml}"
: "${DSC_FILE:=${PROJECT_ROOT}/config/overlays/odh-test/dsc.yaml}"

DSCI_NAME=$(yq '.metadata.name' "$DSCI_FILE")
DESIRED_APP_NS=$(yq '.spec.applicationsNamespace' "$DSCI_FILE")
DESIRED_MON_NS=$(yq '.spec.monitoring.namespace' "$DSCI_FILE")

echo "Applying DSCI (${DSCI_NAME}) from ${DSCI_FILE}..."
if oc get dscinitialization "${DSCI_NAME}" &>/dev/null; then
  CURRENT_APP_NS=$(oc get dscinitialization "${DSCI_NAME}" -o jsonpath='{.spec.applicationsNamespace}')
  CURRENT_MON_NS=$(oc get dscinitialization "${DSCI_NAME}" -o jsonpath='{.spec.monitoring.namespace}')
  if [[ "$CURRENT_APP_NS" != "$DESIRED_APP_NS" ]] || [[ "$CURRENT_MON_NS" != "$DESIRED_MON_NS" ]]; then
    echo "DSCI already exists with different immutable values:"
    echo "  applicationsNamespace: $CURRENT_APP_NS (desired: $DESIRED_APP_NS)"
    echo "  monitoring.namespace: $CURRENT_MON_NS (desired: $DESIRED_MON_NS)"
    echo "Keeping existing DSCI unchanged."
  else
    echo "DSCI already exists with matching namespaces, skipping apply."
  fi
else
  oc apply -f "$DSCI_FILE"
fi
APP_NS=$(oc get dscinitialization "${DSCI_NAME}" -o jsonpath='{.spec.applicationsNamespace}')
echo "DSCI ready (applicationsNamespace: $APP_NS), waiting 5s..."
sleep 5

if [[ "$ENABLE_KUEUE" == "true" ]]; then
  KUEUE_RESOURCES="${PROJECT_ROOT}/.vscode/resources"
  CERT_MANAGER_OPERATORGROUP="${KUEUE_RESOURCES}/cert-manager-operatorgroup.yaml"
  CERT_MANAGER_SUBSCRIPTION="${KUEUE_RESOURCES}/cert-manager-subscription.yaml"
  KUEUE_SUBSCRIPTION="${KUEUE_RESOURCES}/kueue-subscription.yaml"

  if oc get csv -n cert-manager-operator 2>/dev/null | grep -q "cert-manager-operator.*Succeeded"; then
    echo "cert-manager Operator already installed, skipping..."
  else
    echo "Installing cert-manager Operator (required by Kueue)..."
    oc create namespace cert-manager-operator --dry-run=client -o yaml | oc apply -f -
    oc apply -f "$CERT_MANAGER_OPERATORGROUP"
    oc apply -f "$CERT_MANAGER_SUBSCRIPTION"

    echo "Waiting for cert-manager InstallPlan (timeout: 60s)..."
    SECONDS=0
    until oc get subscription openshift-cert-manager-operator -n cert-manager-operator -o jsonpath='{.status.installPlanRef.name}' 2>/dev/null | grep -q .; do
      if (( SECONDS > 60 )); then
        echo "ERROR: Timed out waiting for cert-manager InstallPlan"
        exit 1
      fi
      sleep 5
    done

    INSTALL_PLAN=$(oc get subscription openshift-cert-manager-operator -n cert-manager-operator -o jsonpath='{.status.installPlanRef.name}')
    echo "Approving InstallPlan: $INSTALL_PLAN"
    oc patch installplan "$INSTALL_PLAN" -n cert-manager-operator --type merge -p '{"spec":{"approved":true}}'

    echo "Waiting for cert-manager CSV to install (timeout: 300s)..."
    SECONDS=0
    until oc get csv -n cert-manager-operator 2>/dev/null | grep -q "cert-manager-operator.*Succeeded"; do
      if (( SECONDS > 300 )); then
        echo "ERROR: Timed out waiting for cert-manager Operator to install"
        exit 1
      fi
      sleep 10
    done
    echo "cert-manager Operator installed."
  fi

  if oc get csv -n openshift-operators -l operators.coreos.com/kueue-operator.openshift-operators 2>/dev/null | grep -q Succeeded; then
    echo "Kueue Operator already installed, skipping..."
  else
    echo "Installing Red Hat Kueue Operator..."
    oc apply -f "$KUEUE_SUBSCRIPTION"

    echo "Waiting for Kueue InstallPlan (timeout: 60s)..."
    SECONDS=0
    until oc get subscription kueue-operator -n openshift-operators -o jsonpath='{.status.installPlanRef.name}' 2>/dev/null | grep -q .; do
      if (( SECONDS > 60 )); then
        echo "ERROR: Timed out waiting for Kueue InstallPlan"
        exit 1
      fi
      sleep 5
    done

    INSTALL_PLAN=$(oc get subscription kueue-operator -n openshift-operators -o jsonpath='{.status.installPlanRef.name}')
    echo "Approving InstallPlan: $INSTALL_PLAN"
    oc patch installplan "$INSTALL_PLAN" -n openshift-operators --type merge -p '{"spec":{"approved":true}}'

    echo "Waiting for Kueue CSV to install (timeout: 300s)..."
    SECONDS=0
    until oc get csv -n openshift-operators -l operators.coreos.com/kueue-operator.openshift-operators 2>/dev/null | grep -q Succeeded; do
      if (( SECONDS > 300 )); then
        echo "ERROR: Timed out waiting for Kueue Operator to install"
        exit 1
      fi
      sleep 10
    done
    echo "Kueue Operator installed."
  fi
  export KUEUE_STATE="Unmanaged"
else
  export KUEUE_STATE="Removed"
fi

echo "Applying DSC from ${DSC_FILE}..."
envsubst '$KUEUE_STATE' < "$DSC_FILE" | oc apply -f -
echo "DSC applied, waiting for deployment to be created..."
sleep 30

echo "Waiting for kserve-controller-manager to roll out in $APP_NS..."
oc rollout status deployment/kserve-controller-manager -n "$APP_NS" --timeout=300s
echo "kserve-controller-manager is ready"
