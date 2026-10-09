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

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"
source "$SCRIPT_DIR/../version.sh"
# Seconds to sleep after discovery passes (apiserver RESTMapper can lag discovery).
KUADRANT_PRE_CREATE_SLEEP="${KUADRANT_PRE_CREATE_SLEEP:-30}"
# How many times to wait for Ready before delete/recreate and final failure (default: initial + one retry).
KUADRANT_READY_MAX_ATTEMPTS="${KUADRANT_READY_MAX_ATTEMPTS:-2}"
# Seconds to sleep after deleting Kuadrant before recreating (stabilization).
KUADRANT_POST_DELETE_SLEEP="${KUADRANT_POST_DELETE_SLEEP:-15}"
# Per-attempt timeout for oc wait on Kuadrant Ready (two attempts default; use 10m on very slow clusters).
KUADRANT_READY_TIMEOUT="${KUADRANT_READY_TIMEOUT:-5m}"
# Authorino operand log level. Unset means: "info" when LLMInferenceService audit logging is enabled (the
# identity fields are emitted at info level), otherwise "debug", the pre-existing CI setting. Set it to
# force either, e.g. AUTHORINO_LOG_LEVEL=debug when diagnosing auth failures with audit logging on.
AUTHORINO_LOG_LEVEL="${AUTHORINO_LOG_LEVEL:-}"
# LLMInferenceService audit logging (RHAISTRAT-1799): opt Authorino into custom logging fields and attach a
# Gateway-scoped TelemetryPolicy. "auto" enables it only when the installed RHCL build exposes the APIs
# (Authorino.spec.enableLoggingFields and TelemetryPolicy.spec.logging) and leaves the Authorino CR as it
# was on older builds; "true" fails setup when they are missing; "false" skips the setup entirely.
LLMISVC_AUDIT_LOGGING="${LLMISVC_AUDIT_LOGGING:-auto}"
TELEMETRY_POLICY_READY_TIMEOUT="${TELEMETRY_POLICY_READY_TIMEOUT:-3m}"
# The inference Gateway is created by infra/deploy.gateway.ingress.sh and referenced by name in
# inferenceservice-config (kserveIngressGateway) and the e2e tests, so the policy targets that fixed object.
INFERENCE_GATEWAY_NS="openshift-ingress"
INFERENCE_GATEWAY_NAME="openshift-ai-inference"
AUDIT_TELEMETRY_POLICY_NAME="${INFERENCE_GATEWAY_NAME}-audit-logging"

create_kuadrant_cr() {
  oc create -f - <<EOF
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: ${KUADRANT_NS}
EOF
}

echo "⏳ Installing RHCL(Kuadrant) operator"
oc create ns ${KUADRANT_NS} || true

{
cat <<EOF | oc create -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: ${RHCL_NAME}
  namespace: ${KUADRANT_NS}
spec:
  channel: ${RHCL_CHANNEL}
  installPlanApproval: Automatic
  name: ${RHCL_NAME}
  source: redhat-operators
  sourceNamespace: openshift-marketplace
---
kind: OperatorGroup
apiVersion: operators.coreos.com/v1
metadata:
  name: kuadrant
  namespace: ${KUADRANT_NS}
spec:
  upgradeStrategy: Default
EOF
} || true

wait_for_subscription_csv "${RHCL_NAME}" "${KUADRANT_NS}" 600
wait_for_crd  kuadrants.kuadrant.io  90s
# Let apiserver discovery include kuadrants before first reconcile creates child resources with owner refs.
wait_for_api_discovery "kuadrant.io/v1beta1" "kuadrants" 120

echo "⏳ sleeping ${KUADRANT_PRE_CREATE_SLEEP}s after discovery (RESTMapper can trail discovery)…"
sleep "${KUADRANT_PRE_CREATE_SLEEP}"

create_kuadrant_cr || true

kuadrant_ready_attempt=1
while (( kuadrant_ready_attempt <= KUADRANT_READY_MAX_ATTEMPTS )); do
  echo "⏳ waiting for Kuadrant Ready (attempt ${kuadrant_ready_attempt}/${KUADRANT_READY_MAX_ATTEMPTS}, timeout ${KUADRANT_READY_TIMEOUT})…"
  if oc wait Kuadrant -n "${KUADRANT_NS}" kuadrant --for=condition=Ready --timeout="${KUADRANT_READY_TIMEOUT}"; then
    break
  fi
  if (( kuadrant_ready_attempt >= KUADRANT_READY_MAX_ATTEMPTS )); then
    oc get Kuadrant -n "${KUADRANT_NS}" kuadrant -oyaml
    oc get pods -n "${KUADRANT_NS}" -oyaml
    oc get deployments -n "${KUADRANT_NS}" -oyaml
    oc get csv -n "${KUADRANT_NS}" -oyaml

    oc describe Kuadrant -n "${KUADRANT_NS}" kuadrant
    oc describe pods -n "${KUADRANT_NS}"
    oc describe deployments -n "${KUADRANT_NS}"
    oc describe csv -n "${KUADRANT_NS}"

    echo "=== Controller manager logs ==="
    oc logs -n "${KUADRANT_NS}" deployment/kuadrant-operator-controller-manager --tail=200 || true
    exit 1
  fi
  echo "Kuadrant not Ready; deleting and recreating CR to trigger a new Create reconcile (helps operator versions that only subscribe to Create)…"
  oc delete kuadrant kuadrant -n "${KUADRANT_NS}" --ignore-not-found=true --wait=true --timeout=300s
  echo "⏳ sleeping ${KUADRANT_POST_DELETE_SLEEP}s before recreating Kuadrant…"
  sleep "${KUADRANT_POST_DELETE_SLEEP}"
  create_kuadrant_cr || true
  kuadrant_ready_attempt=$((kuadrant_ready_attempt + 1))
done

wait_for_pod_ready "${KUADRANT_NS}" "control-plane=authorino-operator"

# Wait for service to be created
echo "⏳ waiting for authorino service to be created..."
cert_secret="authorino-server-cert"
oc wait --for=jsonpath='{.metadata.name}'=authorino-authorino-authorization svc/authorino-authorino-authorization -n "${KUADRANT_NS}" --timeout=2m

oc annotate svc/authorino-authorino-authorization  service.beta.openshift.io/serving-cert-secret-name="${cert_secret}" -n "${KUADRANT_NS}"

# Decide whether the installed build supports LLMInferenceService audit logging. Both CRDs exist once
# Kuadrant is Ready, so the check is done once here and reused for the Authorino CR and TelemetryPolicy.
audit_logging_supported() {
  crd_has_spec_field authorinos.operator.authorino.kuadrant.io enableLoggingFields \
    && crd_has_spec_field telemetrypolicies.extensions.kuadrant.io logging
}

AUDIT_LOGGING_ENABLED=false
case "${LLMISVC_AUDIT_LOGGING}" in
  true|auto)
    if audit_logging_supported; then
      AUDIT_LOGGING_ENABLED=true
    elif [[ "${LLMISVC_AUDIT_LOGGING}" == "true" ]]; then
      echo "ERROR: LLMISVC_AUDIT_LOGGING=true but the installed RHCL build exposes neither Authorino.spec.enableLoggingFields nor TelemetryPolicy.spec.logging" >&2
      print_audit_logging_diagnostics
      exit 1
    else
      echo "ℹ️  installed RHCL build has no TelemetryPolicy logging support; skipping LLMInferenceService audit logging setup"
    fi
    ;;
  false) ;;
  *)
    echo "ERROR: LLMISVC_AUDIT_LOGGING must be auto, true or false (got '${LLMISVC_AUDIT_LOGGING}')" >&2
    exit 1
    ;;
esac

# Update Authorino to configure SSL and, when the build supports it, production JSON logging with custom
# logging fields at info level. Without support the CR is rendered as it always was (debug, no logMode).
if [[ "${AUDIT_LOGGING_ENABLED}" == "true" ]]; then
  : "${AUTHORINO_LOG_LEVEL:=info}"
else
  : "${AUTHORINO_LOG_LEVEL:=debug}"
fi
oc apply -f - <<EOF
apiVersion: operator.authorino.kuadrant.io/v1beta1
kind: Authorino
metadata:
  name: authorino
  namespace: ${KUADRANT_NS}
spec:
  replicas: 1
  clusterWide: true
  logLevel: ${AUTHORINO_LOG_LEVEL}
$(if [[ "${AUDIT_LOGGING_ENABLED}" == "true" ]]; then cat <<FIELDS
  logMode: production
  enableLoggingFields: true
FIELDS
fi)
  listener:
    tls:
      enabled: true
      certSecretRef:
        name: authorino-server-cert
  oidcServer:
    tls:
      enabled: false
EOF

wait_for_pod_ready "${KUADRANT_NS}" "control-plane=authorino-operator"

# authorino-operator renders the CR spec into the operand's container args (--log-level=<level>,
# --log-mode=<mode>, --enable-logging-fields). Wait until the Deployment template carries the requested
# values before `oc rollout status`, otherwise the latter can return immediately against the previous,
# still-available Deployment generation. Already-matching args (reruns) pass straight through.
authorino_deployment_matches_spec() {
  local args
  args=$(oc get deployment/authorino -n "${KUADRANT_NS}" -o jsonpath='{.spec.template.spec.containers[0].args[*]}' 2>/dev/null) || return 1
  args=" ${args} "
  [[ "${args}" == *" --log-level=${AUTHORINO_LOG_LEVEL} "* ]] || return 1
  if [[ "${AUDIT_LOGGING_ENABLED}" == "true" ]]; then
    [[ "${args}" == *" --log-mode=production "* ]] || return 1
    [[ "${args}" == *" --enable-logging-fields "* ]] || return 1
  fi
  return 0
}

echo "⏳ waiting for the authorino Deployment to reflect the applied spec…"
authorino_spec_wait=0
until authorino_deployment_matches_spec; do
  if (( authorino_spec_wait >= 180 )); then
    echo "Timed out waiting for deployment/authorino in ${KUADRANT_NS} to reflect the applied Authorino spec" >&2
    oc get deployment/authorino -n "${KUADRANT_NS}" -o jsonpath='current args: {.spec.template.spec.containers[0].args}{"\n"}' 2>/dev/null || true
    print_audit_logging_diagnostics 200
    exit 1
  fi
  sleep 3
  authorino_spec_wait=$((authorino_spec_wait + 3))
done
echo "⏳ waiting for the authorino operand to roll out…"
oc rollout status deployment/authorino -n "${KUADRANT_NS}" --timeout=5m

if [[ "${AUDIT_LOGGING_ENABLED}" == "true" ]]; then
  echo "⏳ Creating audit logging TelemetryPolicy ${INFERENCE_GATEWAY_NS}/${AUDIT_TELEMETRY_POLICY_NAME} for Gateway ${INFERENCE_GATEWAY_NAME}"
  # Values are CEL over Kuadrant well-known attributes. The gateway AuthPolicy authenticates with
  # kubernetesTokenReview, so the identity object is a TokenReviewStatus and the principal lives at
  # auth.identity.user.username (auth.identity.sub is OIDC-only and would never resolve here). Authorino
  # prefixes every key with "custom." in the emitted record and omits keys whose expression cannot be
  # resolved, so client_anonymous is present only for auth-disabled (anonymous) requests.
  oc apply -f - <<EOF
apiVersion: extensions.kuadrant.io/v1alpha1
kind: TelemetryPolicy
metadata:
  name: ${AUDIT_TELEMETRY_POLICY_NAME}
  namespace: ${INFERENCE_GATEWAY_NS}
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: ${INFERENCE_GATEWAY_NAME}
  logging:
    default:
      fields:
        client_identity: auth.identity.user.username
        client_anonymous: auth.identity.anonymous
        request_method: request.method
        request_path: request.path
EOF
  for cond in Accepted Enforced; do
    echo "⏳ waiting for TelemetryPolicy ${cond}=True (timeout ${TELEMETRY_POLICY_READY_TIMEOUT})…"
    if ! oc wait telemetrypolicy.extensions.kuadrant.io "${AUDIT_TELEMETRY_POLICY_NAME}" -n "${INFERENCE_GATEWAY_NS}" \
        --for=condition="${cond}" --timeout="${TELEMETRY_POLICY_READY_TIMEOUT}"; then
      echo "ERROR: TelemetryPolicy ${INFERENCE_GATEWAY_NS}/${AUDIT_TELEMETRY_POLICY_NAME} did not reach ${cond}=True" >&2
      oc get telemetrypolicy.extensions.kuadrant.io "${AUDIT_TELEMETRY_POLICY_NAME}" -n "${INFERENCE_GATEWAY_NS}" -oyaml || true
      print_audit_logging_diagnostics 200
      exit 1
    fi
  done
  echo "✅ LLMInferenceService audit logging configured (Authorino logging fields + TelemetryPolicy)"
fi

echo "✅ kuadrant(authorino) installed"
