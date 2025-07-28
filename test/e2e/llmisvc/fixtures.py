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

import uuid
import os
import pytest
from typing import List
from kubernetes import client
from kubernetes.client.rest import ApiException
from kserve import KServeClient, constants, V1alpha1LLMInferenceService

KSERVE_PLURAL_LLMINFERENCESERVICECONFIG = "llminferenceserviceconfigs"
KSERVE_TEST_NAMESPACE = "kserve-ci-e2e-test"

LLMINFERENCESERVICE_CONFIGS = {
    "workload-single-cpu": {
        "template": {
            "containers": [
                {
                    "name": "main",
                    "image": "quay.io/pierdipi/vllm-cpu:latest",
                    "env": [{"name": "VLLM_LOGGING_LEVEL", "value": "DEBUG"}],
                    "resources": {
                        "limits": {"cpu": "2", "memory": "10Gi"},
                        "requests": {"cpu": "1", "memory": "8Gi"},
                    },
                    "livenessProbe": {
                        "initialDelaySeconds": 30,
                        "periodSeconds": 30,
                        "timeoutSeconds": 30,
                        "failureThreshold": 5,
                    },
                }
            ]
        },
    },
    "workload-pd-cpu": {
        "template": {
            "initContainers": [
                {
                    "name": "llm-d-routing-sidecar",
                    "image": "ghcr.io/llm-d/llm-d-routing-sidecar:v0.2.0"
                }
            ],
            "containers": [
                {
                    "name": "main",
                    "image": "quay.io/pierdipi/vllm-cpu:latest",
                    "env": [{"name": "VLLM_LOGGING_LEVEL", "value": "DEBUG"}],
                    "resources": {
                        "limits": {"cpu": "2", "memory": "10Gi"},
                        "requests": {"cpu": "1", "memory": "8Gi"},
                    }
                }
            ]
        },
        "prefill": {
            "template": {
                "containers": [
                    {
                        "name": "main",
                        "image": "quay.io/pierdipi/vllm-cpu:latest",
                        "env": [{"name": "VLLM_LOGGING_LEVEL", "value": "DEBUG"}],
                        "resources": {
                            "limits": {"cpu": "2", "memory": "10Gi"},
                            "requests": {"cpu": "1", "memory": "8Gi"},
                        }
                    }
                ]
            }
        }
    },
    "model-fb-opt-125m": {
        "model": {"uri": "hf://facebook/opt-125m", "name": "facebook/opt-125m"},
    },
    "router-managed": {
        "router": {"scheduler": {}, "route": {}, "gateway": {}},
    },
}

@pytest.fixture(scope="function")
def test_case(request):
    tc = request.param

    service_name = generate_service_name(request.node.name, tc.base_refs)
    tc.model_name = get_model_name_from_configs(tc.base_refs)

    # TODO fail early if base_refs does not exist (e.g. mistyped)?

    tc.llm_service = V1alpha1LLMInferenceService(
        api_version="serving.kserve.io/v1alpha1",
        kind="LLMInferenceService",
        metadata=client.V1ObjectMeta(
            name=service_name, namespace=KSERVE_TEST_NAMESPACE
        ),
        spec={
            "baseRefs": [{"name": base_ref} for base_ref in tc.base_refs],
        },
    )

    return tc


@pytest.fixture(scope="session", autouse=True)
def llm_config_factory():
    """Factory for creating/cleaning LLMInferenceServiceConfig once per session."""
    created = []
    client = KServeClient(config_file=os.environ.get("KUBECONFIG", "~/.kube/config"))

    def _create_configs(namespace=KSERVE_TEST_NAMESPACE):
        for name in LLMINFERENCESERVICE_CONFIGS:

            spec = LLMINFERENCESERVICE_CONFIGS[name]

            try:
                get_llmisvc_config(client, name, namespace)
                continue
            except Exception as e:
                is_404_api = (
                    isinstance(e, ApiException) and getattr(e, "status", None) == 404
                )
                is_404_runtime = (
                    isinstance(e, RuntimeError) and "not found" in str(e).lower()
                )
                if not (is_404_api or is_404_runtime):
                    raise

            body = {
                "apiVersion": "serving.kserve.io/v1alpha1",
                "kind": "LLMInferenceServiceConfig",
                "metadata": {"name": name, "namespace": namespace},
                "spec": spec,
            }

            try:
                create_llmisvc_config(client, body, namespace)
                created.append((name, namespace))
            except Exception as e:
                if isinstance(e, ApiException) and getattr(e, "status", None) == 409:
                    continue
                if isinstance(e, RuntimeError) and "already exists" in str(e).lower():
                    continue
                # otherwise, real error
                raise

    yield _create_configs()

    for name, namespace in created:
        try:
            delete_llmisvc_config(client, name, namespace)
        except Exception:
            pass


def get_model_name_from_configs(config_names):
    """Extract model name from model config."""
    for config_name in config_names:
        if config_name.startswith("model-"):
            config = LLMINFERENCESERVICE_CONFIGS[config_name]
            if "model" in config and "name" in config["model"]:
                return config["model"]["name"]
    return "default-model"


def generate_service_name(test_name: str, base_refs: List[str]) -> str:
    base_name = test_name.split("[", 1)[0]
    base_name = base_name.replace("test_", "")
    base_name = base_name.replace("_", "-")
    config_suffix = "-".join(sorted(base_refs))
    test_case = f"{base_name}-{config_suffix}".lower()

    uid = uuid.uuid4().hex[:8]

    max_total = 63
    sep = "-"
    max_test_case = max_total - len(sep) - len(uid)
    test_case = test_case[:max_test_case].rstrip(sep)

    return f"{test_case}{sep}{uid}"


def generate_test_id(test_case) -> str:
    """Generate a test ID from base refs."""
    return "-".join(test_case.base_refs)


def create_llmisvc_config(kserve_client, llm_config, namespace=None):
    version = llm_config["apiVersion"].split("/")[1]

    if namespace is None:
        namespace = llm_config.get("metadata", {}).get("namespace", "default")

    try:
        outputs = kserve_client.api_instance.create_namespaced_custom_object(
            constants.KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICECONFIG,
            llm_config,
        )
        return outputs
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->"
            f"create_namespaced_custom_object for LLMInferenceServiceConfig: {e}"
        ) from e


def delete_llmisvc_config(
    kserve_client, name, namespace, version=constants.KSERVE_V1ALPHA1_VERSION
):
    try:
        return kserve_client.api_instance.delete_namespaced_custom_object(
            constants.KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICECONFIG,
            name,
        )
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->"
            f"delete_namespaced_custom_object for LLMInferenceServiceConfig: {e}"
        ) from e


def get_llmisvc_config(
    kserve_client, name, namespace, version=constants.KSERVE_V1ALPHA1_VERSION
):
    try:
        return kserve_client.api_instance.get_namespaced_custom_object(
            constants.KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICECONFIG,
            name,
        )
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->"
            f"get_namespaced_custom_object for LLMInferenceServiceConfig: {e}"
        ) from e
