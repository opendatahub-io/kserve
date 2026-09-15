# Copyright 2025 The KServe Authors.
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

import json
import logging
import os
import ssl

import pytest
from kserve import KServeClient
from kubernetes import client

from .fixtures import (
    generate_test_id,
    inject_k8s_proxy,
)
from .logging import log_execution
from .diagnostic import collect_diagnostics
from ..common.utils import KSERVE_NAMESPACE
from .test_llm_inference_service import (
    TestCase,
    completions_payload,
    create_llmisvc,
    create_response_assertion,
    delete_llmisvc,
    wait_for_llm_isvc_ready,
    wait_for_model_response,
)

logger = logging.getLogger(__name__)

GO_TLS_CIPHER_SUITES = (
    "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
    "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
    "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
    "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
)

OPENSSL_TLS_CIPHER_SUITES = {
    "ECDHE-RSA-AES128-GCM-SHA256",
    "ECDHE-ECDSA-AES128-GCM-SHA256",
    "ECDHE-RSA-AES256-GCM-SHA384",
    "ECDHE-ECDSA-AES256-GCM-SHA384",
}


def test_converted_cipher_suite_names_are_accepted_by_python_ssl():
    """The OpenSSL names rendered for vLLM must configure Python SSL."""
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.set_ciphers(":".join(OPENSSL_TLS_CIPHER_SUITES))

    configured_ciphers = {
        cipher["name"]
        for cipher in context.get_ciphers()
        if cipher["protocol"] != "TLSv1.3"
    }
    assert configured_ciphers == OPENSSL_TLS_CIPHER_SUITES


def _get_tls_config() -> tuple[bool, str, str]:
    """Read the LLMInferenceService TLS settings from inferenceservice-config."""
    inject_k8s_proxy()
    core_v1 = client.CoreV1Api()
    cm = core_v1.read_namespaced_config_map("inferenceservice-config", KSERVE_NAMESPACE)
    ingress = json.loads(cm.data.get("ingress", "{}"))
    return (
        ingress.get("enableLLMInferenceServiceTLS", False),
        ingress.get("llmInferenceServiceTLSMinVersion", ""),
        ingress.get("llmInferenceServiceTLSCipherSuites", ""),
    )


def _set_tls_profile(min_version: str, cipher_suites: str) -> dict:
    """Set a populated TLS profile and return the original ingress configuration."""
    inject_k8s_proxy()
    core_v1 = client.CoreV1Api()
    cm = core_v1.read_namespaced_config_map("inferenceservice-config", KSERVE_NAMESPACE)
    original = json.loads(cm.data.get("ingress", "{}"))
    updated = dict(original)
    updated["llmInferenceServiceTLSMinVersion"] = min_version
    updated["llmInferenceServiceTLSCipherSuites"] = cipher_suites
    cm.data["ingress"] = json.dumps(updated)
    core_v1.replace_namespaced_config_map(
        "inferenceservice-config", KSERVE_NAMESPACE, cm
    )
    return original


def _restore_tls_config(original: dict) -> None:
    core_v1 = client.CoreV1Api()
    cm = core_v1.read_namespaced_config_map("inferenceservice-config", KSERVE_NAMESPACE)
    cm.data["ingress"] = json.dumps(original)
    core_v1.replace_namespaced_config_map(
        "inferenceservice-config", KSERVE_NAMESPACE, cm
    )


def _list_destination_rules(namespace, label_selector):
    """List Istio DestinationRules matching a label selector."""
    custom_api = client.CustomObjectsApi()
    try:
        result = custom_api.list_namespaced_custom_object(
            group="networking.istio.io",
            version="v1",
            namespace=namespace,
            plural="destinationrules",
            label_selector=label_selector,
        )
        return result.get("items", [])
    except client.rest.ApiException as e:
        if e.status == 404:
            return []
        raise


def _get_secret(namespace, name):
    """Get a Kubernetes secret, returning None if not found."""
    core_v1 = client.CoreV1Api()
    try:
        return core_v1.read_namespaced_secret(name, namespace)
    except client.rest.ApiException as e:
        if e.status == 404:
            return None
        raise


def _get_service(namespace, name):
    """Get a Kubernetes service, returning None if not found."""
    core_v1 = client.CoreV1Api()
    try:
        return core_v1.read_namespaced_service(name, namespace)
    except client.rest.ApiException as e:
        if e.status == 404:
            return None
        raise


def _get_container_commands(namespace, service_name):
    """Return commands for the EPP, routing sidecar, and vLLM workload."""
    core_v1 = client.CoreV1Api()
    pods = core_v1.list_namespaced_pod(
        namespace,
        label_selector=(
            f"app.kubernetes.io/part-of=llminferenceservice,"
            f"app.kubernetes.io/name={service_name}"
        ),
    )

    commands = {}
    for pod in pods.items:
        containers = [
            *(pod.spec.init_containers or []),
            *(pod.spec.containers or []),
        ]
        for container in containers:
            command = [*(container.command or []), *(container.args or [])]
            if "/app/epp" in command:
                commands["epp"] = command
            elif "/app/pd-sidecar" in command:
                commands["sidecar"] = command
            elif any("vllm serve" in arg for arg in command):
                commands["vllm"] = command
    return commands


@pytest.mark.llminferenceservice
@pytest.mark.asyncio(loop_scope="session")
@pytest.mark.parametrize(
    "test_case",
    [
        pytest.param(
            TestCase(
                base_refs=[
                    "router-managed",
                    "workload-pd-cpu",
                    "model-fb-opt-125m",
                ],
                prompt="KServe is a",
                payload_formatter=completions_payload,
                response_assertion=create_response_assertion(with_field="choices"),
                service_name="tls-verification-test",
            ),
            marks=[pytest.mark.cluster_cpu, pytest.mark.cluster_single_node],
        ),
    ],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_llm_tls_resources(test_case: TestCase, request: pytest.FixtureRequest):
    """Verify that TLS-related resources (DestinationRules, cert secrets, service port)
    are correctly present or absent based on the enableLLMInferenceServiceTLS flag."""
    inject_k8s_proxy()

    original_ingress = _set_tls_profile(
        "VersionTLS12",
        ",".join(GO_TLS_CIPHER_SUITES),
    )
    request.addfinalizer(lambda: _restore_tls_config(original_ingress))
    tls_enabled, tls_min_version, tls_cipher_suites = _get_tls_config()
    logger.info(
        "LLMInferenceService TLS config: enabled=%s, min_version=%s, cipher_suites=%s",
        tls_enabled,
        tls_min_version,
        tls_cipher_suites,
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config"),
        client_configuration=client.Configuration(),
    )

    service_name = test_case.llm_service.metadata.name

    try:
        create_llmisvc(kserve_client, test_case.llm_service)
        wait_for_llm_isvc_ready(
            kserve_client, test_case.llm_service, test_case.wait_timeout
        )
        wait_for_model_response(kserve_client, test_case, test_case.wait_timeout)

        _verify_tls_resources(service_name, test_case.namespace, tls_enabled)
        _verify_tls_arguments(
            service_name,
            test_case.namespace,
            tls_min_version,
            tls_cipher_suites,
        )

    except Exception as e:
        logger.error(f"Failed TLS verification for {service_name}: {e}")
        collect_diagnostics(
            service_name,
            test_case.llm_service.metadata.namespace,
            kserve_client=kserve_client,
            log=logger.info,
        )
        raise
    finally:
        try:
            if os.getenv("SKIP_RESOURCE_DELETION", "False").lower() in (
                "false",
                "0",
                "f",
            ):
                delete_llmisvc(kserve_client, test_case.llm_service)
        except Exception as e:
            logger.warning(f"Warning: Failed to cleanup service {service_name}: {e}")


def _verify_tls_resources(service_name, namespace, tls_enabled):
    """Assert TLS resource state matches the enableLLMInferenceServiceTLS flag."""
    label_selector = (
        f"app.kubernetes.io/part-of=llminferenceservice,"
        f"app.kubernetes.io/name={service_name},"
        f"llm-d.ai/managed=true"
    )

    dest_rules = _list_destination_rules(namespace, label_selector)
    cert_secret = _get_secret(namespace, f"{service_name}-kserve-self-signed-certs")
    workload_svc = _get_service(namespace, f"{service_name}-kserve-workload-svc")

    if tls_enabled:
        assert len(dest_rules) > 0, (
            f"Expected DestinationRules to exist when TLS is enabled, but found none "
            f"(label_selector={label_selector})"
        )
        for dr in dest_rules:
            tls_settings = dr.get("spec", {}).get("trafficPolicy", {}).get("tls", {})
            assert tls_settings.get("mode") == "SIMPLE", (
                f"DestinationRule {dr['metadata']['name']} should use SIMPLE TLS mode, "
                f"got: {tls_settings}"
            )

        assert cert_secret is not None, (
            "Expected self-signed cert secret to exist when TLS is enabled"
        )
        assert "tls.crt" in cert_secret.data, "Cert secret missing tls.crt"
        assert "tls.key" in cert_secret.data, "Cert secret missing tls.key"

        assert workload_svc is not None, "Workload service should exist"
        port = workload_svc.spec.ports[0]
        assert port.name == "https", (
            f"Workload service port name should be 'https' when TLS enabled, got '{port.name}'"
        )
        assert port.app_protocol == "https", (
            f"Workload service appProtocol should be 'https' when TLS enabled, got '{port.app_protocol}'"
        )
    else:
        assert len(dest_rules) == 0, (
            f"Expected no DestinationRules when TLS is disabled, but found {len(dest_rules)}: "
            f"{[dr['metadata']['name'] for dr in dest_rules]}"
        )

        # Cert secret may or may not exist (Option 2: kept from previous TLS=on state).
        # We only verify it is NOT actively reconciled by checking DestinationRules and ports.

        assert workload_svc is not None, "Workload service should exist"
        port = workload_svc.spec.ports[0]
        assert port.name == "http", (
            f"Workload service port name should be 'http' when TLS disabled, got '{port.name}'"
        )
        assert port.app_protocol == "http", (
            f"Workload service appProtocol should be 'http' when TLS disabled, got '{port.app_protocol}'"
        )

    logger.info(
        f"TLS resource verification passed (tls_enabled={tls_enabled}, "
        f"dest_rules={len(dest_rules)}, "
        f"cert_secret={'present' if cert_secret else 'absent'}, "
        f"svc_port={workload_svc.spec.ports[0].name if workload_svc else 'N/A'})"
    )


def _verify_tls_arguments(service_name, namespace, tls_min_version, tls_cipher_suites):
    """Assert that the TLS profile is propagated to the EPP and routing sidecar."""
    commands = _get_container_commands(namespace, service_name)
    assert "epp" in commands, "Expected to find the EPP container command"
    assert "sidecar" in commands, "Expected to find the routing sidecar command"
    assert "vllm" in commands, "Expected to find the vLLM workload command"

    expected_args = {
        "--tls-min-version": tls_min_version,
        "--tls-cipher-suites": tls_cipher_suites,
    }
    for component in ("epp", "sidecar"):
        command = commands[component]
        for flag, value in expected_args.items():
            matching_args = [arg for arg in command if arg.startswith(f"{flag}=")]
            if value:
                assert matching_args == [f"{flag}={value}"], (
                    f"Expected {component} to receive {flag}={value}, "
                    f"got: {matching_args}"
                )
            else:
                assert not matching_args, (
                    f"Expected {component} to omit {flag}, got: {matching_args}"
                )

    vllm_command = " ".join(commands["vllm"])
    if tls_cipher_suites:
        for cipher_suite in OPENSSL_TLS_CIPHER_SUITES:
            assert cipher_suite in vllm_command, (
                f"Expected vLLM command to contain {cipher_suite}, got: {vllm_command}"
            )
        assert "--ssl-ciphers" in vllm_command
    else:
        assert "--ssl-ciphers" not in vllm_command

    logger.info("TLS argument verification passed for components: %s", sorted(commands))
