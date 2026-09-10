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

"""E2E tests for the ODH CPU accelerator LLMInferenceServiceConfig preset.

These tests exercise the preset shipped by the ODH overlay
(kserve-config-llm-template-cpu, installed in the system namespace) through the
real cluster path: services in a test namespace reference it via
spec.baseRefs, relying on the controller's system-namespace fallback. The
preset is discovered rather than assumed, since operator-managed stacks may
stamp a version suffix onto shipped config names. On stacks without the
preset (non-ODH), all tests skip.
"""

from __future__ import annotations

import os
import pytest
from kserve import KServeClient, V1alpha1LLMInferenceService, constants
from kubernetes import client

from .fixtures import (
    VLLM_CPU_IMAGE,
    find_system_llmisvc_config,
    generate_test_id,
    inject_k8s_proxy,
)
from .logging import log_execution
from .test_llm_inference_service import (
    TestCase,
    completions_payload,
    create_llmisvc,
    create_response_assertion,
    maybe_delete_llmisvc,
    wait_for,
)
from .test_llm_inference_service import (
    test_llm_inference_service as run_llmisvc_test_case,
)
from ..common.utils import KSERVE_NAMESPACE

pytestmark = [pytest.mark.cluster_cpu, pytest.mark.cluster_single_node]

CPU_PRESET_NAME = "kserve-config-llm-template-cpu"
API_VERSION = "v1alpha2"
DEPLOYMENT_WAIT_SECONDS = 300


def _get_cpu_preset(kserve_client: KServeClient) -> dict:
    """Discover the shipped CPU preset in the system namespace, or skip."""
    preset = find_system_llmisvc_config(kserve_client, CPU_PRESET_NAME)
    if preset is None:
        pytest.skip(
            f"{CPU_PRESET_NAME} not found in {KSERVE_NAMESPACE}; "
            "CPU accelerator preset requires the ODH overlay"
        )
    return preset


def _preset_main_container(preset: dict) -> dict:
    containers = preset["spec"]["template"]["containers"]
    main = next((c for c in containers if c.get("name") == "main"), None)
    assert main is not None, f"{CPU_PRESET_NAME} has no 'main' container"
    return main


def _cpu_preset_llmisvc(
    name: str, namespace: str, preset_name: str, template: dict | None = None
) -> V1alpha1LLMInferenceService:
    spec: dict = {
        "baseRefs": [{"name": preset_name}],
        "model": {
            "uri": "hf://facebook/opt-125m",
            "name": "facebook/opt-125m",
        },
        "replicas": 1,
    }
    if template is not None:
        spec["template"] = template
    return V1alpha1LLMInferenceService(
        api_version=f"{constants.KSERVE_GROUP}/{API_VERSION}",
        kind="LLMInferenceService",
        metadata=client.V1ObjectMeta(name=name, namespace=namespace),
        spec=spec,
    )


def _wait_for_workload_deployment(namespace: str, service_name: str):
    apps_v1 = client.AppsV1Api()
    deployment_name = f"{service_name}-kserve"

    def assert_deployment_exists():
        try:
            return apps_v1.read_namespaced_deployment(deployment_name, namespace)
        except client.rest.ApiException as e:
            raise AssertionError(
                f"Deployment {deployment_name} not found in {namespace}: {e.status}"
            ) from e

    return wait_for(
        assert_deployment_exists, timeout=DEPLOYMENT_WAIT_SECONDS, interval=5.0
    )


def _main_container(deployment) -> client.V1Container:
    main = next(
        (c for c in deployment.spec.template.spec.containers if c.name == "main"),
        None,
    )
    assert main is not None, "workload Deployment has no 'main' container"
    return main


def _assert_preset_env(deployment) -> None:
    env = {e.name: e.value for e in (_main_container(deployment).env or [])}
    assert env.get("VLLM_CPU_KVCACHE_SPACE") == "4", (
        f"VLLM_CPU_KVCACHE_SPACE not merged from preset, env: {env}"
    )
    assert env.get("OMP_NUM_THREADS") == "4", (
        f"OMP_NUM_THREADS not merged from preset, env: {env}"
    )


@log_execution
def test_cpu_accelerator_preset_applies_to_workload(test_namespace):
    """The shipped CPU preset, referenced via baseRefs, configures the workload."""
    inject_k8s_proxy()
    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config"),
        client_configuration=client.Configuration(),
    )

    preset = _get_cpu_preset(kserve_client)
    preset_image = _preset_main_container(preset)["image"]
    llm_isvc = _cpu_preset_llmisvc(
        "cpu-accel-preset", test_namespace, preset["metadata"]["name"]
    )

    create_llmisvc(kserve_client, llm_isvc)
    test_failed = False
    try:
        deployment = _wait_for_workload_deployment(
            test_namespace, llm_isvc.metadata.name
        )

        main = _main_container(deployment)
        assert main.image == preset_image, (
            f"workload image {main.image} does not match preset image {preset_image}"
        )
        _assert_preset_env(deployment)

        node_selector = deployment.spec.template.spec.node_selector or {}
        assert node_selector.get("kubernetes.io/arch") == "amd64", (
            f"arch nodeSelector not merged from preset, got: {node_selector}"
        )
    except Exception:
        test_failed = True
        raise
    finally:
        maybe_delete_llmisvc(kserve_client, llm_isvc, test_failed)


@log_execution
def test_cpu_accelerator_preset_user_image_wins(test_namespace):
    """A user-specified image overrides the preset image; env defaults still merge."""
    inject_k8s_proxy()
    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config"),
        client_configuration=client.Configuration(),
    )

    preset = _get_cpu_preset(kserve_client)
    llm_isvc = _cpu_preset_llmisvc(
        "cpu-accel-precedence",
        test_namespace,
        preset["metadata"]["name"],
        template={"containers": [{"name": "main", "image": VLLM_CPU_IMAGE}]},
    )

    create_llmisvc(kserve_client, llm_isvc)
    test_failed = False
    try:
        deployment = _wait_for_workload_deployment(
            test_namespace, llm_isvc.metadata.name
        )

        main = _main_container(deployment)
        assert main.image == VLLM_CPU_IMAGE, (
            f"user image should win over preset, got {main.image}"
        )
        _assert_preset_env(deployment)
    except Exception:
        test_failed = True
        raise
    finally:
        maybe_delete_llmisvc(kserve_client, llm_isvc, test_failed)


@pytest.mark.parametrize(
    "test_case",
    [
        pytest.param(
            TestCase(
                base_refs=[
                    "router-managed",
                    "model-fb-opt-125m",
                ],
                external_base_refs=[CPU_PRESET_NAME],
                endpoint="/v1/completions",
                prompt="KServe is a",
                payload_formatter=completions_payload,
                response_assertion=create_response_assertion(with_field="choices"),
            ),
        ),
    ],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_cpu_accelerator_preset_serves_inference(test_case: TestCase):
    """Full e2e through the shipped preset: deploy, reach Ready, serve a completion."""
    run_llmisvc_test_case(test_case)
