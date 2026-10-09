# Copyright 2026 The KServe Authors.
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

"""
E2E coverage for LLMInferenceService audit logging (RHAISTRAT-1799 / RHOAIENG-96170).

The audit trail is produced by Authorino (RHCL) for every request that passes the gateway
AuthPolicy. A Gateway-scoped TelemetryPolicy adds CEL-derived fields to Authorino's
info-level "outgoing authorization response" records under the ``custom.`` prefix.
The CI setup (``infra/deploy.kuadrant.sh``) creates that policy with four fields:
``client_identity``, ``client_anonymous``, ``request_method`` and ``request_path``.

These tests drive requests through real LLMInferenceServices and assert on the records
Authorino emits for them. Requests are tagged with a unique ``?audit=<id>`` query string;
``request.path`` as logged keeps the query string, which is how a test finds its own records
on a shared Authorino.

The module skips itself when the installed RHCL build has no TelemetryPolicy logging
support or the CI policy is absent (older builds), so the suite stays green on clusters
where the feature is not available.
"""

import json
import os
import time
import uuid
from dataclasses import dataclass, field
from typing import Callable, Dict, List, Optional, Tuple

import pytest
import requests
from kserve import KServeClient
from kubernetes import client

from .diagnostic import collect_diagnostics
from .fixtures import (  # noqa: F401
    LLMINFERENCESERVICE_CONFIGS,
    generate_test_id,
    test_case,  # noqa: F811
)
from .logging import log_execution, logger
from .test_llm_auth import (
    cleanup_service_account,
    create_service_account_with_inference_access,
    get_service_account_token,
)
from .test_llm_inference_service import (
    TestCase,
    create_llmisvc,
    delete_llmisvc,
    get_llm_service_url,
    wait_for,
    wait_for_llm_isvc_ready,
)

# Where the RHCL / Kuadrant operand lives and how the CI setup names the audit policy.
# Defaults mirror test/scripts/openshift-ci/infra/deploy.kuadrant.sh.
AUTHORINO_NAMESPACE = os.environ.get("AUTHORINO_NAMESPACE", "kuadrant-system")
AUTHORINO_CR_NAME = os.environ.get("AUTHORINO_CR_NAME", "authorino")
AUTHORINO_POD_SELECTOR = os.environ.get(
    "AUTHORINO_POD_SELECTOR", "authorino-resource=authorino"
)
AUDIT_GATEWAY_NAMESPACE = os.environ.get("AUDIT_GATEWAY_NAMESPACE", "openshift-ingress")
AUDIT_GATEWAY_NAME = os.environ.get("AUDIT_GATEWAY_NAME", "openshift-ai-inference")
AUDIT_TELEMETRY_POLICY = os.environ.get(
    "AUDIT_TELEMETRY_POLICY", f"{AUDIT_GATEWAY_NAME}-audit-logging"
)
# The EnvoyFilter Kuadrant renders for the gateway; its generation is the data-plane view
# of the policies (status conditions can report Enforced before anything is programmed).
AUDIT_ENVOYFILTER = f"kuadrant-{AUDIT_GATEWAY_NAME}"

TELEMETRY_POLICY_GVR = ("extensions.kuadrant.io", "v1alpha1", "telemetrypolicies")
AUTHORINO_GVR = ("operator.authorino.kuadrant.io", "v1beta1", "authorinos")
ENVOYFILTER_GVR = ("networking.istio.io", "v1alpha3", "envoyfilters")
AUTHPOLICY_GVR = ("kuadrant.io", "v1", "authpolicies")
CSV_GVR = ("operators.coreos.com", "v1alpha1", "clusterserviceversions")

# Field names the CI TelemetryPolicy configures (see deploy.kuadrant.sh) and the prefix
# Authorino puts in front of every configured field in the emitted record.
CUSTOM_PREFIX = "custom."
FIELD_IDENTITY = f"{CUSTOM_PREFIX}client_identity"
FIELD_ANONYMOUS = f"{CUSTOM_PREFIX}client_anonymous"
FIELD_METHOD = f"{CUSTOM_PREFIX}request_method"
FIELD_PATH = f"{CUSTOM_PREFIX}request_path"
BASELINE_FIELDS = (
    "client_identity",
    "client_anonymous",
    "request_method",
    "request_path",
)

# Probe fields added by the security-control tests only. Prefixed so that cleanup can
# remove exactly these keys without touching the baseline policy.
#
# ``request.*`` attributes are resolved by the wasm shim before the request reaches Authorino,
# and a failing expression (for instance a header that is absent) fails the request with a 500
# instead of omitting the field (CONNLINK-1887). The sensitive probe is therefore guarded with
# ``has()`` so that requests without an Authorization header keep flowing while it is installed.
PROBE_PREFIX = "e2e_probe_"
PROBE_SENSITIVE_FIELD = f"{PROBE_PREFIX}sensitive"
PROBE_UNRESOLVABLE_FIELD = f"{PROBE_PREFIX}unresolvable"
PROBE_ABSENT_VALUE = "absent"
PROBE_FIELDS = {
    PROBE_SENSITIVE_FIELD: (
        "has(request.headers.authorization) ? request.headers.authorization : "
        f'"{PROBE_ABSENT_VALUE}"'
    ),
    # ``auth.*`` expressions are deferred to Authorino, which omits the ones it cannot resolve.
    PROBE_UNRESOLVABLE_FIELD: "auth.identity.e2e_does_not_exist",
}
REDACTED_VALUE = "***REDACTED***"
TRUNCATION_SUFFIX = "...(truncated)"
# Authorino's --logging-fields-max-value-bytes default; the Authorino CR may override it.
DEFAULT_MAX_VALUE_BYTES = 1024

AUTHORINO_DECISION_MSG = "outgoing authorization response"
# How long to wait for a record to show up in Authorino's log after a request. Config
# propagation (TelemetryPolicy -> EnvoyFilter -> wasm -> Authorino) can lag by tens of seconds.
RECORD_WAIT_SECONDS = int(os.environ.get("AUDIT_RECORD_WAIT_SECONDS", "150"))
POLL_INTERVAL = 3


# ---------------------------------------------------------------------------
# Cluster introspection helpers (all go through the session KServeClient)
# ---------------------------------------------------------------------------


@dataclass
class AuditEnvironment:
    """What the cluster offers for audit logging; filled by ``audit_environment``."""

    __test__ = False

    policy_fields: Dict[str, str] = field(default_factory=dict)
    max_value_bytes: int = DEFAULT_MAX_VALUE_BYTES
    versions: Dict[str, str] = field(default_factory=dict)


def _get_object(
    kserve_client: KServeClient, gvr, namespace: str, name: str
) -> Optional[dict]:
    group, version, plural = gvr
    try:
        return kserve_client.api_instance.get_namespaced_custom_object(
            group, version, namespace, plural, name
        )
    except client.rest.ApiException as e:
        if e.status == 404:
            return None
        raise


def _crd_has_spec_field(crd_name: str, field_name: str) -> bool:
    """True when any served version of the CRD declares ``.spec.<field_name>``."""
    try:
        crd = client.ApiextensionsV1Api().read_custom_resource_definition(crd_name)
    except client.rest.ApiException:
        return False
    for version in crd.spec.versions or []:
        schema = version.schema.open_apiv3_schema if version.schema else None
        spec = (schema.properties or {}).get("spec") if schema else None
        if spec and spec.properties and field_name in spec.properties:
            return True
    return False


def _get_authorino_cr(kserve_client: KServeClient) -> Optional[dict]:
    return _get_object(
        kserve_client, AUTHORINO_GVR, AUTHORINO_NAMESPACE, AUTHORINO_CR_NAME
    )


def _get_telemetry_policy(kserve_client: KServeClient) -> Optional[dict]:
    return _get_object(
        kserve_client,
        TELEMETRY_POLICY_GVR,
        AUDIT_GATEWAY_NAMESPACE,
        AUDIT_TELEMETRY_POLICY,
    )


def _get_envoyfilter(kserve_client: KServeClient) -> Optional[dict]:
    return _get_object(
        kserve_client, ENVOYFILTER_GVR, AUDIT_GATEWAY_NAMESPACE, AUDIT_ENVOYFILTER
    )


def _conditions(obj: Optional[dict]) -> Dict[str, str]:
    conditions = ((obj or {}).get("status") or {}).get("conditions") or []
    return {c.get("type"): c.get("status") for c in conditions}


def _policy_fields(policy: Optional[dict]) -> Dict[str, str]:
    logging_spec = ((policy or {}).get("spec") or {}).get("logging") or {}
    return dict((logging_spec.get("default") or {}).get("fields") or {})


def _envoyfilter_generation(kserve_client: KServeClient) -> Optional[int]:
    ef = _get_envoyfilter(kserve_client)
    return ef.get("metadata", {}).get("generation") if ef else None


def _envoyfilter_mentions(kserve_client: KServeClient, substring: str) -> bool:
    """True when the rendered wasm config for the gateway contains ``substring``."""
    ef = _get_envoyfilter(kserve_client)
    return bool(ef) and substring in json.dumps(ef.get("spec", {}))


def _patch_telemetry_policy_fields(
    kserve_client: KServeClient, fields: Dict[str, Optional[str]]
) -> None:
    """Merge-patch ``spec.logging.default.fields``; ``None`` values delete a key."""
    group, version, plural = TELEMETRY_POLICY_GVR
    kserve_client.api_instance.patch_namespaced_custom_object(
        group,
        version,
        AUDIT_GATEWAY_NAMESPACE,
        plural,
        AUDIT_TELEMETRY_POLICY,
        {"spec": {"logging": {"default": {"fields": fields}}}},
    )


def _recreate_telemetry_policy(kserve_client: KServeClient, original: dict) -> None:
    """Delete the audit TelemetryPolicy and re-create it from ``original``'s spec.

    Removing keys from ``spec.logging.default.fields`` leaves their bindings in the rendered
    EnvoyFilter until the policy itself is deleted, so this is the only reliable way back to
    the baseline configuration (CONNLINK-1888).
    """
    group, version, plural = TELEMETRY_POLICY_GVR
    api = kserve_client.api_instance
    try:
        api.delete_namespaced_custom_object(
            group, version, AUDIT_GATEWAY_NAMESPACE, plural, AUDIT_TELEMETRY_POLICY
        )
    except client.rest.ApiException as e:
        if e.status != 404:
            raise
    try:
        wait_for(
            lambda: _assert_not(
                _envoyfilter_mentions(kserve_client, "io.kuadrant.logging.fields"),
                "logging fields still rendered after policy deletion",
            ),
            timeout=120,
            interval=POLL_INTERVAL,
        )
    except AssertionError as e:
        logger.warning(f"{e}; re-creating the policy anyway")
    body = {
        "apiVersion": original["apiVersion"],
        "kind": original["kind"],
        "metadata": {
            "name": AUDIT_TELEMETRY_POLICY,
            "namespace": AUDIT_GATEWAY_NAMESPACE,
            "labels": original["metadata"].get("labels") or {},
            "annotations": {
                k: v
                for k, v in (original["metadata"].get("annotations") or {}).items()
                if not k.startswith("kubectl.kubernetes.io/")
            },
        },
        "spec": original["spec"],
    }
    api.create_namespaced_custom_object(
        group, version, AUDIT_GATEWAY_NAMESPACE, plural, body
    )
    wait_for(
        lambda: _assert_equal(
            _conditions(_get_telemetry_policy(kserve_client)).get("Enforced"),
            "True",
            "re-created TelemetryPolicy not Enforced",
        ),
        timeout=120,
        interval=POLL_INTERVAL,
    )
    wait_for(
        lambda: _assert_true(
            _envoyfilter_mentions(kserve_client, "client_identity"),
            "baseline fields not rendered after re-creating the policy",
        ),
        timeout=120,
        interval=POLL_INTERVAL,
    )


def _assert_true(value, message: str):
    assert value, message


def _assert_not(value, message: str):
    assert not value, message


def _assert_equal(value, expected, message: str):
    assert value == expected, f"{message}: {value!r}"


def _deployment_image(kserve_client: KServeClient, name: str) -> str:
    """``image (imageID)`` of a deployment's first container, or ``absent``."""
    try:
        deploy = kserve_client.app_api.read_namespaced_deployment(
            name, AUTHORINO_NAMESPACE
        )
    except client.rest.ApiException:
        return "absent"
    image = deploy.spec.template.spec.containers[0].image
    selector = ",".join(
        f"{k}={v}" for k, v in deploy.spec.selector.match_labels.items()
    )
    pods = kserve_client.core_api.list_namespaced_pod(
        AUTHORINO_NAMESPACE, label_selector=selector
    ).items
    for pod in pods:
        for status in pod.status.container_statuses or []:
            if status.image_id:
                return f"{image} ({status.image_id})"
    return image


def record_component_versions(kserve_client: KServeClient) -> Dict[str, str]:
    """Log and return the RHCL / Kuadrant / Authorino versions the run uses (AC: evidence)."""
    versions: Dict[str, str] = {}
    group, version, plural = CSV_GVR
    try:
        csvs = kserve_client.api_instance.list_namespaced_custom_object(
            group, version, AUTHORINO_NAMESPACE, plural
        )
        for csv in csvs.get("items", []):
            name = csv["metadata"]["name"]
            if any(k in name for k in ("rhcl", "kuadrant", "authorino", "limitador")):
                versions[f"csv/{name}"] = (csv.get("status") or {}).get("phase", "")
    except client.rest.ApiException as e:
        logger.warning(f"Could not list CSVs in {AUTHORINO_NAMESPACE}: {e}")

    for deploy in (
        "kuadrant-operator-controller-manager",
        "authorino-operator",
        AUTHORINO_CR_NAME,
    ):
        versions[f"deployment/{deploy}"] = _deployment_image(kserve_client, deploy)

    spec = (_get_authorino_cr(kserve_client) or {}).get("spec", {})
    versions["authorino.spec"] = (
        f"logLevel={spec.get('logLevel')} logMode={spec.get('logMode')} "
        f"enableLoggingFields={spec.get('enableLoggingFields')} "
        f"loggingFieldsMaxValueBytes={spec.get('loggingFieldsMaxValueBytes')}"
    )

    logger.info("Audit logging component versions:")
    for key, value in versions.items():
        logger.info(f"  {key}: {value}")
    return versions


def audit_logging_unavailable_reason(kserve_client: KServeClient) -> Optional[str]:
    """Why audit logging cannot be tested on this cluster, or None when it can."""
    if not _crd_has_spec_field("telemetrypolicies.extensions.kuadrant.io", "logging"):
        return "TelemetryPolicy CRD has no spec.logging (RHCL build predates CONNLINK-1384)"
    if not _crd_has_spec_field(
        "authorinos.operator.authorino.kuadrant.io", "enableLoggingFields"
    ):
        return "Authorino CRD has no spec.enableLoggingFields"
    authorino = _get_authorino_cr(kserve_client)
    if authorino is None:
        return f"Authorino CR {AUTHORINO_NAMESPACE}/{AUTHORINO_CR_NAME} not found"
    if not authorino.get("spec", {}).get("enableLoggingFields"):
        return "Authorino CR does not set spec.enableLoggingFields=true"
    policy = _get_telemetry_policy(kserve_client)
    if policy is None:
        return (
            f"TelemetryPolicy {AUDIT_GATEWAY_NAMESPACE}/{AUDIT_TELEMETRY_POLICY} not found "
            "(CI setup did not create it)"
        )
    if not _policy_fields(policy):
        return "TelemetryPolicy has no logging.default.fields"
    return None


def print_audit_diagnostics(
    kserve_client: KServeClient,
    log: Callable = logger.info,
    marker: Optional[str] = None,
):
    """Enough to tell policy propagation, CEL evaluation and logging failures apart."""
    log("# Audit logging diagnostics")
    policy = _get_telemetry_policy(kserve_client)
    if policy is None:
        log(
            "TelemetryPolicy %s/%s: not found",
            AUDIT_GATEWAY_NAMESPACE,
            AUDIT_TELEMETRY_POLICY,
        )
    else:
        log(
            "TelemetryPolicy %s: generation=%s conditions=%s fields=%s",
            AUDIT_TELEMETRY_POLICY,
            policy["metadata"].get("generation"),
            _conditions(policy),
            _policy_fields(policy),
        )
    log("Authorino CR spec: %s", (_get_authorino_cr(kserve_client) or {}).get("spec"))
    try:
        deploy = kserve_client.app_api.read_namespaced_deployment(
            AUTHORINO_CR_NAME, AUTHORINO_NAMESPACE
        )
        log(
            "Authorino deployment args: %s",
            deploy.spec.template.spec.containers[0].args,
        )
    except client.rest.ApiException as e:
        log("Authorino deployment: %s", e.reason)
    log(
        "EnvoyFilter %s generation: %s",
        AUDIT_ENVOYFILTER,
        _envoyfilter_generation(kserve_client),
    )
    group, version, plural = AUTHPOLICY_GVR
    try:
        policies = kserve_client.api_instance.list_cluster_custom_object(
            group, version, plural
        )
        for ap in policies.get("items", []):
            log(
                "AuthPolicy %s/%s: %s",
                ap["metadata"]["namespace"],
                ap["metadata"]["name"],
                _conditions(ap),
            )
    except client.rest.ApiException as e:
        log("AuthPolicies: %s", e.reason)
    records = read_authorino_decision_records(
        kserve_client.core_api, since_seconds=600, marker=marker
    )
    log(
        "Authorino decision records in the last 10m%s: %d",
        f" for marker {marker}" if marker else "",
        len(records),
    )
    for rec in records[-20:]:
        log("  %s", json.dumps(sanitize_record(rec), sort_keys=True))


# ---------------------------------------------------------------------------
# Authorino log access
# ---------------------------------------------------------------------------


def sanitize_record(record: dict) -> dict:
    """Keep only the decision and the custom fields; drop anything that could carry secrets."""
    keep = {"ts", "level", "authorized", "response", "request id"}
    return {k: v for k, v in record.items() if k in keep or k.startswith(CUSTOM_PREFIX)}


def read_authorino_decision_records(
    core_api: client.CoreV1Api, since_seconds: int, marker: Optional[str] = None
) -> List[dict]:
    """Decision records from every Authorino replica, optionally filtered by the audit marker."""
    pods = core_api.list_namespaced_pod(
        AUTHORINO_NAMESPACE, label_selector=AUTHORINO_POD_SELECTOR
    ).items
    records: List[dict] = []
    for pod in pods:
        try:
            # Ask for the raw response: with preloaded content some client versions hand back the
            # repr of the bytes payload as one string, which cannot be split into JSON lines.
            response = core_api.read_namespaced_pod_log(
                name=pod.metadata.name,
                namespace=AUTHORINO_NAMESPACE,
                since_seconds=since_seconds,
                _preload_content=False,
            )
            raw = response.data.decode("utf-8", errors="replace")
        except client.rest.ApiException as e:
            logger.warning(f"Could not read logs of {pod.metadata.name}: {e.reason}")
            continue
        for line in raw.splitlines():
            if AUTHORINO_DECISION_MSG not in line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            if rec.get("msg") != AUTHORINO_DECISION_MSG:
                continue
            if marker and marker not in str(rec.get(FIELD_PATH, "")):
                continue
            records.append(rec)
    records.sort(key=lambda r: r.get("ts", ""))
    return records


def wait_for_decision_record(
    core_api: client.CoreV1Api,
    marker: str,
    predicate: Callable[[dict], bool] = lambda _: True,
    timeout: int = RECORD_WAIT_SECONDS,
) -> dict:
    """Return the first decision record carrying ``marker`` that satisfies ``predicate``."""

    def find_record() -> dict:
        seen = read_authorino_decision_records(
            core_api, since_seconds=timeout + 120, marker=marker
        )
        for rec in seen:
            if predicate(rec):
                return rec
        raise AssertionError(
            f"no Authorino decision record matching marker {marker!r} yet; "
            f"records seen for the marker: {[sanitize_record(r) for r in seen]}"
        )

    return wait_for(find_record, timeout=timeout, interval=POLL_INTERVAL)


def audit_url(service_url: str, path: str, marker: str) -> str:
    return f"{service_url}{path}?audit={marker}"


def send_and_fetch_record(
    core_api: client.CoreV1Api,
    url: str,
    marker: str,
    headers: Optional[Dict[str, str]] = None,
    expected_status: Optional[List[int]] = None,
    retries: int = 24,
) -> Tuple[requests.Response, dict]:
    """GET ``url`` (tagged with ``marker``) and return the response with its Authorino record.

    Retries on 401/403 when a 200 is expected (RBAC and policy propagation lag), the same
    way the auth suite does.
    """
    response = None
    for attempt in range(retries):
        response = requests.get(url, headers=headers or {}, timeout=60)
        if expected_status is None or response.status_code in expected_status:
            break
        if 200 in expected_status and response.status_code in (401, 403):
            logger.info(
                f"Attempt {attempt + 1}: {response.status_code}, waiting for propagation..."
            )
            time.sleep(5)
            continue
        break
    if expected_status is not None:
        assert response.status_code in expected_status, (
            f"GET {url}: expected {expected_status}, got {response.status_code}: "
            f"{response.text[:300]}"
        )
    # Each attempt produces its own record; match the one with the final decision.
    allowed = response.status_code == 200
    record = wait_for_decision_record(
        core_api, marker, predicate=lambda r: (r.get("authorized") is True) == allowed
    )
    logger.info(
        f"Audit record for {marker}: {json.dumps(sanitize_record(record), sort_keys=True)}"
    )
    return response, record


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(scope="module")
def audit_environment(kserve_client: KServeClient) -> AuditEnvironment:
    """Skip the module when the cluster cannot do audit logging; record versions otherwise."""
    reason = audit_logging_unavailable_reason(kserve_client)
    if reason:
        pytest.skip(f"LLMInferenceService audit logging not available: {reason}")

    policy = _get_telemetry_policy(kserve_client)
    conditions = _conditions(policy)
    assert conditions.get("Accepted") == "True", (
        f"TelemetryPolicy {AUDIT_TELEMETRY_POLICY} not Accepted: {conditions}"
    )
    env = AuditEnvironment(
        policy_fields=_policy_fields(policy),
        versions=record_component_versions(kserve_client),
    )
    authorino_spec = (_get_authorino_cr(kserve_client) or {}).get("spec", {})
    env.max_value_bytes = int(
        authorino_spec.get("loggingFieldsMaxValueBytes") or DEFAULT_MAX_VALUE_BYTES
    )
    logger.info(
        f"Audit TelemetryPolicy fields: {env.policy_fields}; "
        f"max value bytes: {env.max_value_bytes}"
    )
    missing = [f for f in BASELINE_FIELDS if f not in env.policy_fields]
    assert not missing, (
        f"CI TelemetryPolicy is missing fields {missing}: {env.policy_fields}"
    )
    return env


@pytest.fixture
def probe_fields(kserve_client: KServeClient, audit_environment: AuditEnvironment):
    """Add the security-control probe fields to the gateway policy for one test, then remove them.

    Only the ``e2e_probe_*`` keys are added and deleted, so the baseline fields other tests
    (possibly running on another xdist worker) rely on are never touched. Cleanup verifies the
    rendered gateway config dropped the probes and falls back to re-creating the policy when the
    operator kept stale bindings around.
    """
    original = _get_telemetry_policy(kserve_client)
    assert original is not None, f"TelemetryPolicy {AUDIT_TELEMETRY_POLICY} disappeared"
    _patch_telemetry_policy_fields(kserve_client, dict(PROBE_FIELDS))
    logger.info(f"Added probe fields to {AUDIT_TELEMETRY_POLICY}: {PROBE_FIELDS}")
    try:
        yield PROBE_FIELDS
    finally:
        try:
            _patch_telemetry_policy_fields(
                kserve_client, {k: None for k in PROBE_FIELDS}
            )
            try:
                wait_for(
                    lambda: _assert_not(
                        _envoyfilter_mentions(kserve_client, PROBE_PREFIX),
                        "probe bindings still rendered",
                    ),
                    timeout=60,
                    interval=POLL_INTERVAL,
                )
                logger.info(f"Removed probe fields from {AUDIT_TELEMETRY_POLICY}")
            except AssertionError:
                logger.warning(
                    "Probe bindings survived the field removal in the rendered EnvoyFilter; "
                    "re-creating the TelemetryPolicy from its original spec"
                )
                _recreate_telemetry_policy(kserve_client, original)
                logger.info(f"Re-created {AUDIT_TELEMETRY_POLICY} without probe fields")
        except client.rest.ApiException as e:
            logger.warning(f"Failed to remove probe fields: {e}")


def _run_with_service(
    kserve_client: KServeClient,
    test_case: TestCase,  # noqa: F811
    body: Callable[[str, str], None],
):
    """Create the test LLMInferenceService, run ``body(service_url, ns)``, clean up."""
    service_name = test_case.llm_service.metadata.name
    ns = test_case.llm_service.metadata.namespace
    test_failed = False
    try:
        create_llmisvc(kserve_client, test_case.llm_service)
        wait_for_llm_isvc_ready(
            kserve_client, test_case.llm_service, test_case.wait_timeout
        )
        service_url = get_llm_service_url(kserve_client, test_case.llm_service)
        body(service_url, ns)
    except Exception as e:
        test_failed = True
        logger.error(f"❌ ERROR: audit logging test failed for {service_name}: {e}")
        collect_diagnostics(
            service_name, ns, kserve_client=kserve_client, log=logger.info
        )
        print_audit_diagnostics(
            kserve_client, log=logger.info, marker=f"/{ns}/{service_name}/"
        )
        raise
    finally:
        truthy = ("true", "1", "t")
        skip_deletion = os.getenv(
            "SKIP_RESOURCE_DELETION", "False"
        ).lower() in truthy or (
            os.getenv("SKIP_DELETION_ON_FAILURE", "False").lower() in truthy
            and test_failed
        )
        if not skip_deletion:
            try:
                delete_llmisvc(kserve_client, test_case.llm_service)
            except Exception as e:
                logger.warning(f"⚠️ Failed to clean up {service_name}: {e}")


def _enable_auth(test_case: TestCase, enabled: bool):  # noqa: F811
    if not test_case.llm_service.metadata.annotations:
        test_case.llm_service.metadata.annotations = {}
    test_case.llm_service.metadata.annotations[
        "security.opendatahub.io/enable-auth"
    ] = "true" if enabled else "false"


def _simulator_case(service_name: str, case_id: str):
    return pytest.param(
        TestCase(
            base_refs=["router-managed", "workload-llmd-simulator"],
            prompt="audit",
            service_name=service_name,
        ),
        marks=[
            pytest.mark.cluster_cpu,
            pytest.mark.cluster_single_node,
            pytest.mark.llmd_simulator,
        ],
        id=case_id,
    )


def _wait_for_probe_record(
    core_api: client.CoreV1Api, service_url: str, headers: Dict[str, str]
) -> dict:
    """Send authenticated requests until a record carries the probe fields.

    Probe fields reach Authorino only after the policy change propagates through the
    EnvoyFilter and wasm config, which takes tens of seconds.
    """
    sensitive_key = f"{CUSTOM_PREFIX}{PROBE_SENSITIVE_FIELD}"

    def probe() -> dict:
        marker = f"probe-{uuid.uuid4().hex[:8]}"
        _, rec = send_and_fetch_record(
            core_api,
            audit_url(service_url, "/v1/models", marker),
            marker,
            headers=headers,
            expected_status=[200],
        )
        assert sensitive_key in rec, (
            f"probe fields not in the record yet: {sanitize_record(rec)}"
        )
        return rec

    return wait_for(probe, timeout=RECORD_WAIT_SECONDS, interval=5)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@pytest.mark.auth
@pytest.mark.audit_logging
@pytest.mark.parametrize(
    "test_case",
    [_simulator_case("audit-auth", "audit-auth-enabled")],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_llm_audit_records_for_authenticated_requests(
    kserve_client: KServeClient,
    test_case: TestCase,  # noqa: F811
    audit_environment: AuditEnvironment,
):
    """Allowed, unauthenticated, invalid-token and authorization-denied requests all produce
    a decision record; identity is present exactly when authentication succeeded."""
    _enable_auth(test_case, True)
    service_name = test_case.llm_service.metadata.name
    sa_name = f"{service_name}-audit-sa"
    norbac_sa = f"{service_name}-norbac-sa"
    core = kserve_client.core_api

    def body(service_url: str, ns: str):
        try:
            token = create_service_account_with_inference_access(
                kserve_client, sa_name, service_name, namespace=ns
            )
            # A second SA that authenticates but has no RBAC on the service: authz denied.
            core.create_namespaced_service_account(
                namespace=ns,
                body=client.V1ServiceAccount(
                    metadata=client.V1ObjectMeta(name=norbac_sa)
                ),
            )
            norbac_token = get_service_account_token(kserve_client, norbac_sa, ns)
            expected_identity = f"system:serviceaccount:{ns}:{sa_name}"

            # 1) allowed
            marker = f"allowed-{uuid.uuid4().hex[:8]}"
            _, rec = send_and_fetch_record(
                core,
                audit_url(service_url, "/v1/models", marker),
                marker,
                headers={"Authorization": f"Bearer {token}"},
                expected_status=[200],
            )
            assert rec.get("authorized") is True
            assert rec.get(FIELD_IDENTITY) == expected_identity, rec
            assert rec.get(FIELD_METHOD) == "GET", rec
            assert f"/{ns}/{service_name}/v1/models?audit={marker}" in rec.get(
                FIELD_PATH, ""
            ), rec
            assert FIELD_ANONYMOUS not in rec, (
                f"anonymous flag on an authenticated request: {rec}"
            )
            logger.info("✅ allowed request carries identity, method and path")

            # 2) unauthenticated: no token
            marker = f"notoken-{uuid.uuid4().hex[:8]}"
            _, rec = send_and_fetch_record(
                core,
                audit_url(service_url, "/v1/models", marker),
                marker,
                expected_status=[401, 403],
            )
            assert rec.get("authorized") is False
            assert FIELD_IDENTITY not in rec, (
                f"identity on an unauthenticated request: {rec}"
            )
            assert rec.get(FIELD_METHOD) == "GET" and marker in rec.get(
                FIELD_PATH, ""
            ), rec
            logger.info("✅ unauthenticated request still records method and path")

            # 3) invalid token
            marker = f"badtoken-{uuid.uuid4().hex[:8]}"
            _, rec = send_and_fetch_record(
                core,
                audit_url(service_url, "/v1/models", marker),
                marker,
                headers={"Authorization": "Bearer not.a.valid.token"},
                expected_status=[401, 403],
            )
            assert rec.get("authorized") is False and FIELD_IDENTITY not in rec, rec
            logger.info("✅ invalid token request recorded without identity")

            # 4) authenticated but not authorized: identity must still be recorded
            marker = f"denied-{uuid.uuid4().hex[:8]}"
            _, rec = send_and_fetch_record(
                core,
                audit_url(service_url, "/v1/models", marker),
                marker,
                headers={"Authorization": f"Bearer {norbac_token}"},
                expected_status=[403],
            )
            assert rec.get("authorized") is False
            assert (
                rec.get(FIELD_IDENTITY) == f"system:serviceaccount:{ns}:{norbac_sa}"
            ), rec
            logger.info("✅ authorization denial keeps the identity in the record")
        finally:
            cleanup_service_account(kserve_client, sa_name, namespace=ns)
            try:
                core.delete_namespaced_service_account(name=norbac_sa, namespace=ns)
            except client.rest.ApiException:
                pass

    _run_with_service(kserve_client, test_case, body)


@pytest.mark.auth
@pytest.mark.audit_logging
@pytest.mark.parametrize(
    "test_case",
    [_simulator_case("audit-noauth", "audit-auth-disabled")],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_llm_audit_records_anonymous_when_auth_disabled(
    kserve_client: KServeClient,
    test_case: TestCase,  # noqa: F811
    audit_environment: AuditEnvironment,
):
    """With ``enable-auth=false`` the record flags the client as anonymous and carries no identity."""
    _enable_auth(test_case, False)
    service_name = test_case.llm_service.metadata.name

    def body(service_url: str, ns: str):
        marker = f"anon-{uuid.uuid4().hex[:8]}"
        # The anonymous AuthPolicy override is created by the platform when it sees the
        # annotation; send_and_fetch_record retries on 401/403 until it has propagated.
        _, rec = send_and_fetch_record(
            kserve_client.core_api,
            audit_url(service_url, "/v1/models", marker),
            marker,
            expected_status=[200],
        )
        assert rec.get("authorized") is True
        assert rec.get(FIELD_ANONYMOUS) == "true", rec
        assert FIELD_IDENTITY not in rec, (
            f"identity recorded for an anonymous request: {rec}"
        )
        assert rec.get(FIELD_METHOD) == "GET", rec
        assert f"/{ns}/{service_name}/" in rec.get(FIELD_PATH, ""), rec
        logger.info("✅ auth-disabled request recorded as anonymous")

    _run_with_service(kserve_client, test_case, body)


@pytest.mark.auth
@pytest.mark.audit_logging
@pytest.mark.parametrize(
    "test_case",
    [_simulator_case("audit-controls", "audit-security-controls")],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_llm_audit_security_controls(
    kserve_client: KServeClient,
    test_case: TestCase,  # noqa: F811
    audit_environment: AuditEnvironment,
    probe_fields,
):
    """Unresolvable fields are omitted and oversized values are truncated, without side effects."""
    _enable_auth(test_case, True)
    service_name = test_case.llm_service.metadata.name
    sa_name = f"{service_name}-audit-sa"
    max_bytes = audit_environment.max_value_bytes
    unresolvable_key = f"{CUSTOM_PREFIX}{PROBE_UNRESOLVABLE_FIELD}"
    core = kserve_client.core_api

    def body(service_url: str, ns: str):
        try:
            token = create_service_account_with_inference_access(
                kserve_client, sa_name, service_name, namespace=ns
            )
            headers = {"Authorization": f"Bearer {token}"}
            rec = _wait_for_probe_record(core, service_url, headers)

            # Unresolvable CEL: key omitted, record otherwise intact.
            assert unresolvable_key not in rec, f"unresolvable field rendered: {rec}"
            assert rec.get(FIELD_IDENTITY) == f"system:serviceaccount:{ns}:{sa_name}", (
                rec
            )
            logger.info(
                "✅ unresolvable expression is omitted without affecting other fields"
            )

            # A request without the probed header must still be served (guarded expression).
            marker = f"noheader-{uuid.uuid4().hex[:8]}"
            _, rec = send_and_fetch_record(
                core,
                audit_url(service_url, "/v1/models", marker),
                marker,
                expected_status=[401, 403],
            )
            assert (
                rec.get(f"{CUSTOM_PREFIX}{PROBE_SENSITIVE_FIELD}") == PROBE_ABSENT_VALUE
            ), rec
            logger.info(
                "✅ guarded header expression resolves for header-less requests"
            )

            # Truncation: a path longer than the configured limit is cut and suffixed.
            marker = f"long-{uuid.uuid4().hex[:8]}"
            filler = "x" * (max_bytes + 200)
            response = requests.get(
                f"{service_url}/v1/models/{filler}?audit={marker}",
                headers=headers,
                timeout=60,
            )
            # The simulator may 404 the unknown model; the decision record is what matters.
            assert response.status_code in (200, 404), response.status_code
            rec = wait_for_decision_record(
                core, "/v1/models/" + filler[:32], predicate=lambda r: FIELD_PATH in r
            )
            path = rec[FIELD_PATH]
            assert path.endswith(TRUNCATION_SUFFIX), (
                f"long path not truncated: ...{path[-80:]}"
            )
            assert len(path) == max_bytes + len(TRUNCATION_SUFFIX), (
                f"truncated length {len(path)} != limit {max_bytes} + suffix"
            )
            logger.info(f"✅ oversized value truncated at {max_bytes} bytes")
        finally:
            cleanup_service_account(kserve_client, sa_name, namespace=ns)

    _run_with_service(kserve_client, test_case, body)


@pytest.mark.auth
@pytest.mark.audit_logging
@pytest.mark.xfail(
    strict=True,
    reason=(
        "request.* logging fields are resolved by the wasm shim and sent to Authorino as plain "
        "strings, so Authorino's header redaction never applies and credential headers are "
        "logged in clear; tracked in CONNLINK-1884. Remove the xfail once Kuadrant redacts "
        "or rejects header-valued logging fields."
    ),
)
@pytest.mark.parametrize(
    "test_case",
    [_simulator_case("audit-redaction", "audit-redaction")],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_llm_audit_redacts_request_header_values(
    kserve_client: KServeClient,
    test_case: TestCase,  # noqa: F811
    audit_environment: AuditEnvironment,
    probe_fields,
):
    """A logging field that reads the Authorization header must not expose the credential."""
    _enable_auth(test_case, True)
    service_name = test_case.llm_service.metadata.name
    sa_name = f"{service_name}-audit-sa"
    sensitive_key = f"{CUSTOM_PREFIX}{PROBE_SENSITIVE_FIELD}"

    def body(service_url: str, ns: str):
        try:
            token = create_service_account_with_inference_access(
                kserve_client, sa_name, service_name, namespace=ns
            )
            rec = _wait_for_probe_record(
                kserve_client.core_api,
                service_url,
                {"Authorization": f"Bearer {token}"},
            )
            assert token not in json.dumps(rec), (
                "bearer token written in clear to the audit record"
            )
            assert rec[sensitive_key] == REDACTED_VALUE, (
                f"header value not redacted: {str(rec[sensitive_key])[:16]}..."
            )
            logger.info("✅ sensitive header value is redacted")
        finally:
            cleanup_service_account(kserve_client, sa_name, namespace=ns)

    _run_with_service(kserve_client, test_case, body)


@pytest.mark.audit_logging
@pytest.mark.cluster_cpu
@pytest.mark.cluster_single_node
@log_execution
def test_llm_audit_logging_config_is_stable(
    kserve_client: KServeClient, audit_environment: AuditEnvironment
):
    """The rendered gateway config must converge: a TelemetryPolicy with several fields once made
    the operator rewrite the EnvoyFilter on every reconcile (Kuadrant/kuadrant-operator#2357)."""
    policy_before = (
        (_get_telemetry_policy(kserve_client) or {})
        .get("metadata", {})
        .get("resourceVersion")
    )
    before = _envoyfilter_generation(kserve_client)
    if before is None:
        pytest.skip(
            f"EnvoyFilter {AUDIT_ENVOYFILTER} not present (no HTTPRoute attached to the gateway yet)"
        )
    time.sleep(30)
    after = _envoyfilter_generation(kserve_client)
    policy_after = (
        (_get_telemetry_policy(kserve_client) or {})
        .get("metadata", {})
        .get("resourceVersion")
    )
    if policy_before != policy_after:
        pytest.skip(
            "TelemetryPolicy changed during the window (another test is patching it)"
        )
    logger.info(
        f"EnvoyFilter {AUDIT_ENVOYFILTER} generation {before} -> {after} over 30s"
    )
    assert after == before, (
        f"EnvoyFilter {AUDIT_ENVOYFILTER} generation moved {before} -> {after} in 30s with an "
        "unchanged TelemetryPolicy: the operator is rewriting the gateway config in a loop"
    )
