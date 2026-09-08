"""E2E tests for tracing endpoint synchronization across versioned presets."""

import copy
import json
import time

import pytest

from conftest import (
    KSERVE_CR_NAME,
    LLMISVC_CONFIG_RESOURCE,
    NAMESPACE,
    OPERATOR_DEPLOYMENT,
    TIMEOUT_120S,
    get_cr,
    get_jsonpath,
    run,
    trigger_reconcile,
    wait_for,
)


MONITORING_RESOURCE = "monitorings.services.platform.opendatahub.io"
MONITORING_NAME = "default-monitoring"
TRACING_PRESET_SUFFIX = "kserve-config-llm-tracing"
HISTORICAL_PRESET_PREFIX = "e2e-v0-0-0"
PLATFORM_SAMPLE_RATIO = "0.271"
HISTORICAL_SAMPLE_RATIO = "0.739"
UPSTREAM_ENDPOINT = "http://otel-collector:4317"


def _get_tracing_presets(kubectl):
    result = run(
        [
            kubectl,
            "get",
            LLMISVC_CONFIG_RESOURCE,
            "-n",
            NAMESPACE,
            "-o",
            "json",
        ]
    )
    items = json.loads(result.stdout).get("items", [])
    return {
        item["metadata"]["name"]: item
        for item in items
        if item["metadata"]["name"].endswith(TRACING_PRESET_SUFFIX)
    }


def _tracing_spec(preset):
    return preset.get("spec", {}).get("tracing", {})


def _monitoring(kubectl):
    result = run(
        [kubectl, "get", MONITORING_RESOURCE, MONITORING_NAME, "-o", "json"],
        check=False,
    )
    if result.returncode != 0:
        stderr = result.stderr.lower()
        if (
            "not found" in stderr
            or "no matches for kind" in stderr
            or "doesn't have a resource type" in stderr
        ):
            return None
        raise RuntimeError(
            f"Failed to get {MONITORING_RESOURCE}/{MONITORING_NAME}: {result.stderr}"
        )
    return json.loads(result.stdout)


def _configured_collector_endpoint(kubectl):
    monitoring_namespace = get_jsonpath(
        kubectl,
        "deployment",
        OPERATOR_DEPLOYMENT,
        "{.spec.template.spec.containers[*].env[?(@.name=='MONITORING_NAMESPACE')].value}",
        namespace=NAMESPACE,
    )
    monitoring_namespace = (
        monitoring_namespace.split()[0] if monitoring_namespace else NAMESPACE
    )
    return f"http://data-science-collector-collector.{monitoring_namespace}.svc:4317"


def _current_preset_name(kubectl):
    version = get_jsonpath(
        kubectl,
        "configmap",
        "odh-kserve-config",
        "{.data.platformVersion}",
        namespace=NAMESPACE,
    )
    if not version:
        cr = get_cr(kubectl, check=False) or {}
        version = cr.get("metadata", {}).get("annotations", {}).get(
            "platform.opendatahub.io/version", ""
        )
    prefix = f"v{version.replace('.', '-')}-" if version else "v0-0-0-"
    return f"{prefix}{TRACING_PRESET_SUFFIX}"


def _assert_preset_state(kubectl, name, endpoint, sample_ratio=None):
    presets = _get_tracing_presets(kubectl)
    assert name in presets, f"tracing preset {name} was not found: {list(presets)}"
    tracing = _tracing_spec(presets[name])
    assert tracing.get("exporterEndpoint") == endpoint
    if sample_ratio is not None:
        assert tracing.get("samplerArg") == sample_ratio


@pytest.mark.tracing
class TestTracingPresetSynchronization:
    """Verify tracing changes through the deployed controller and API server."""

    def test_monitoring_updates_current_and_historical_presets(
        self, kubectl, apply_kserve_cr
    ):
        monitoring = _monitoring(kubectl)

        current_name = _current_preset_name(kubectl)
        presets = _get_tracing_presets(kubectl)
        if current_name not in presets:
            pytest.skip(f"current tracing preset {current_name} is not installed")

        endpoint = _configured_collector_endpoint(kubectl)

        historical_name = (
            f"{HISTORICAL_PRESET_PREFIX}-{int(time.time())}-{TRACING_PRESET_SUFFIX}"
        )
        historical = copy.deepcopy(presets[current_name])
        historical["metadata"]["name"] = historical_name
        for field in (
            "resourceVersion",
            "uid",
            "managedFields",
            "creationTimestamp",
            "generation",
            "ownerReferences",
        ):
            historical["metadata"].pop(field, None)
        historical.pop("status", None)
        historical["spec"]["tracing"]["exporterEndpoint"] = UPSTREAM_ENDPOINT
        historical["spec"]["tracing"]["samplerArg"] = HISTORICAL_SAMPLE_RATIO

        original_spec = copy.deepcopy(monitoring.get("spec", {})) if monitoring else None
        try:
            run([kubectl, "apply", "-f", "-"], input_text=json.dumps(historical))
            if monitoring:
                run(
                    [
                        kubectl,
                        "patch",
                        MONITORING_RESOURCE,
                        MONITORING_NAME,
                        "--type",
                        "merge",
                        "-p",
                        json.dumps(
                            {"spec": {"traces": {"sampleRatio": PLATFORM_SAMPLE_RATIO}}}
                        ),
                    ]
                )
                trigger_reconcile(kubectl, name=KSERVE_CR_NAME, trigger_id="tracing-enabled")

                wait_for(
                    lambda: _assert_preset_state(
                        kubectl, current_name, endpoint, PLATFORM_SAMPLE_RATIO
                    ),
                    timeout=TIMEOUT_120S,
                    interval=5,
                )
                wait_for(
                    lambda: _assert_preset_state(
                        kubectl, historical_name, endpoint, HISTORICAL_SAMPLE_RATIO
                    ),
                    timeout=TIMEOUT_120S,
                    interval=5,
                )

                run(
                    [
                        kubectl,
                        "patch",
                        MONITORING_RESOURCE,
                        MONITORING_NAME,
                        "--type",
                        "merge",
                        "-p",
                        json.dumps({"spec": {"traces": None}}),
                    ]
                )
                trigger_reconcile(kubectl, name=KSERVE_CR_NAME, trigger_id="tracing-disabled")
            else:
                trigger_reconcile(kubectl, name=KSERVE_CR_NAME, trigger_id="tracing-disabled")

            wait_for(
                lambda: _assert_preset_state(kubectl, current_name, UPSTREAM_ENDPOINT),
                timeout=TIMEOUT_120S,
                interval=5,
            )
            wait_for(
                lambda: _assert_preset_state(
                    kubectl, historical_name, UPSTREAM_ENDPOINT, HISTORICAL_SAMPLE_RATIO
                ),
                timeout=TIMEOUT_120S,
                interval=5,
            )
        finally:
            if original_spec is not None:
                restore_spec = copy.deepcopy(original_spec)
                if "traces" not in restore_spec:
                    restore_spec["traces"] = None
                run(
                    [
                        kubectl,
                        "patch",
                        MONITORING_RESOURCE,
                        MONITORING_NAME,
                        "--type",
                        "merge",
                        "-p",
                        json.dumps({"spec": restore_spec}),
                    ]
                )
                trigger_reconcile(
                    kubectl, name=KSERVE_CR_NAME, trigger_id="tracing-restore"
                )
                expected_endpoint = (
                    endpoint
                    if original_spec.get("traces") is not None
                    else UPSTREAM_ENDPOINT
                )
                wait_for(
                    lambda: _assert_preset_state(kubectl, current_name, expected_endpoint),
                    timeout=TIMEOUT_120S,
                    interval=5,
                )
            run(
                [
                    kubectl,
                    "delete",
                    LLMISVC_CONFIG_RESOURCE,
                    historical_name,
                    "-n",
                    NAMESPACE,
                    "--ignore-not-found",
                ],
            )
