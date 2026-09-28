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

import pytest
from kserve import KServeClient
from kubernetes import client

from .fixtures import (
    generate_test_id,
    inject_k8s_proxy,
)
from .logging import log_execution
from .diagnostic import collect_diagnostics
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


def _is_tls_enabled() -> bool:
    """Read the enableLLMInferenceServiceTLS flag from the inferenceservice-config ConfigMap."""
    inject_k8s_proxy()
    core_v1 = client.CoreV1Api()
    kserve_ns = os.environ.get("KSERVE_NAMESPACE", "opendatahub")
    cm = core_v1.read_namespaced_config_map("inferenceservice-config", kserve_ns)
    ingress = json.loads(cm.data.get("ingress", "{}"))
    return ingress.get("enableLLMInferenceServiceTLS", False)


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


@pytest.mark.llminferenceservice
@pytest.mark.asyncio(loop_scope="session")
@pytest.mark.parametrize(
    "test_case",
    [
        pytest.param(
            TestCase(
                base_refs=[
                    "router-managed",
                    "workload-single-cpu",
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
def test_llm_tls_resources(test_case: TestCase):
    """Verify that TLS-related resources (DestinationRules, cert secrets, service port)
    are correctly present or absent based on the enableLLMInferenceServiceTLS flag."""
    inject_k8s_proxy()

    tls_enabled = _is_tls_enabled()
    logger.info(f"enableLLMInferenceServiceTLS = {tls_enabled}")

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
