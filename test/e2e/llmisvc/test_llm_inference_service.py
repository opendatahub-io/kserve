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
import os
import time

import pytest
import requests
from kubernetes import client
from kserve import KServeClient, V1alpha1LLMInferenceService, constants

from .test_configs import (
    LLMINFERENCESERVICE_CONFIGS,
    generate_test_id,
    llm_config_factory,
    KSERVE_TEST_NAMESPACE,
)

KSERVE_PLURAL_LLMINFERENCESERVICE = "llminferenceservices"


@pytest.mark.llminferenceservice
@pytest.mark.asyncio(scope="session")
@pytest.mark.parametrize(
    "config_names",
    [
        pytest.param(["router-managed", "workload-single-cpu", "model-fb-opt-125m"], marks=pytest.mark.cluster_cpu),
        pytest.param(["router-managed", "workload-amd-gpu", "model-fb-opt-125m"], marks=pytest.mark.cluster_amd),
    ],
    indirect=["config_names"],
    ids=generate_test_id,
)
async def test_llm_inference_service(request, llm_config_factory, config_names):
    created_service_configs = llm_config_factory(config_names)
    service_name = generate_service_name(request.node.name, config_names)

    llm_isvc = V1alpha1LLMInferenceService(
        api_version="serving.kserve.io/v1alpha1",
        kind="LLMInferenceService",
        metadata=client.V1ObjectMeta(
            name=service_name, namespace=KSERVE_TEST_NAMESPACE
        ),
        spec={

            "baseRefs": [{"name": config_name} for config_name in created_service_configs],
        },
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )

    try:
        create_llmisvc(kserve_client, llm_isvc)
        wait_for_model_response(
            kserve_client,
            service_name,
            KSERVE_TEST_NAMESPACE,
            model_name=get_model_name_from_configs(config_names),
        )
    except Exception as e:
        print(f"ERROR: Failed to call llm inference service {service_name}: {e}")
        collect_diagnostics(service_name, KSERVE_TEST_NAMESPACE)
        raise
    finally:
        try:
            delete_llmisvc(kserve_client, service_name, KSERVE_TEST_NAMESPACE)
        except Exception as e:
            print(f"Warning: Failed to cleanup service {service_name}: {e}")


def create_llmisvc(kserve_client, llm_isvc, namespace=None):
    from kserve.utils import utils

    version = llm_isvc.api_version.split("/")[1]

    if namespace is None:
        namespace = utils.get_isvc_namespace(llm_isvc)

    try:
        outputs = kserve_client.api_instance.create_namespaced_custom_object(
            constants.KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICE,
            llm_isvc,
        )
        return outputs
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->"
            f"create_namespaced_custom_object for LLMInferenceService: {e}"
        ) from e


def delete_llmisvc(
    kserve_client, name, namespace, version=constants.KSERVE_V1ALPHA1_VERSION
):
    try:
        return kserve_client.api_instance.delete_namespaced_custom_object(
            constants.KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICE,
            name,
        )
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->"
            f"delete_namespaced_custom_object for LLMInferenceService: {e}"
        ) from e


def get_llmisvc(
    kserve_client, name, namespace, version=constants.KSERVE_V1ALPHA1_VERSION
):
    try:
        return kserve_client.api_instance.get_namespaced_custom_object(
            constants.KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICE,
            name,
        )
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->"
            f"get_namespaced_custom_object for LLMInferenceService: {e}"
        ) from e


def wait_for_model_response(
    kserve_client,
    name,
    namespace,
    timeout_seconds=600,
    version=constants.KSERVE_V1ALPHA1_VERSION,
    model_name=None,
):
    if model_name is None:
        model_name = "default-model"

    service_url = None

    def assert_model_responds():
        nonlocal service_url

        try:
            service_url = get_llm_service_url(kserve_client, name, namespace, version)
        except Exception as e:
            raise AssertionError(f"Failed to get service URL: {e}") from e

        completion_url = f"{service_url}/v1/completions"
        test_payload = {"model": model_name, "prompt": "test", "max_tokens": 1}

        try:
            response = requests.post(
                completion_url,
                headers={"Content-Type": "application/json"},
                json=test_payload,
                timeout=30,
            )
        except Exception as e:
            raise AssertionError(f"Failed to call model: {e}") from e

        assert (
            response.status_code == 200
        ), f"Service returned {response.status_code}: {response.text}"
        return service_url

    return wait_for(assert_model_responds, timeout=timeout_seconds, interval=10.0)


def get_llm_service_url(
    kserve_client, service_name, namespace, version=constants.KSERVE_V1ALPHA1_VERSION
):
    try:
        llm_isvc = get_llmisvc(kserve_client, service_name, namespace, version)

        if "status" not in llm_isvc:
            raise ValueError(f"No status found in LLM inference service {service_name}")

        status = llm_isvc["status"]

        if "url" in status and status["url"]:
            return status["url"]

        if (
            "addresses" in status
            and status["addresses"]
            and len(status["addresses"]) > 0
        ):
            first_address = status["addresses"][0]
            if "url" in first_address:
                return first_address["url"]

        raise ValueError(f"No URL found in LLM inference service {service_name} status")

    except Exception as e:
        raise ValueError(
            f"Failed to get URL for LLM inference service {service_name}: {e}"
        ) from e


def wait_for(assertion_fn, timeout: float = 5.0, interval: float = 0.1):
    deadline = time.time() + timeout
    while True:
        try:
            return assertion_fn()
        except AssertionError:
            if time.time() >= deadline:
                raise
            time.sleep(interval)


def get_model_name_from_configs(config_names):
    """Extract model name from model config."""
    for config_name in config_names:
        if config_name.startswith("model-"):
            config = LLMINFERENCESERVICE_CONFIGS[config_name]
            if "model" in config and "name" in config["model"]:
                return config["model"]["name"]
    return "default-model"


def generate_service_name(test_name, config_names):
    base_name = test_name.split("[")[0]  # Remove everything after [
    base_name = base_name.replace("test_", "").replace("_", "-")
    config_suffix = "-".join(sorted(config_names))
    service_name = f"{base_name}-{config_suffix}"
    service_name = service_name.lower()
    service_name = service_name[:63].rstrip("-")
    return service_name


def collect_diagnostics(service_name, namespace):
    try:
        kserve_client = KServeClient(
            config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
        )

        print(f"\n{'='*60}")
        print(f"DIAGNOSTIC INFORMATION FOR {service_name} in {namespace}")
        print(f"{'='*60}")

        print("\n--- LLM Inference Service ---")
        try:
            llm_isvc = get_llmisvc(kserve_client, service_name, namespace)
            print(json.dumps(llm_isvc, indent=2, default=str))
        except Exception as e:
            print(f"Failed to get LLM inference service: {e}")

        print("\n--- Events ---")
        try:
            core_v1 = client.CoreV1Api()
            events = core_v1.list_namespaced_event(
                namespace=namespace,
                field_selector=f"involvedObject.name={service_name}",
            )
            if events.items:
                sorted_events = sorted(
                    events.items,
                    key=lambda x: x.last_timestamp or x.first_timestamp,
                    reverse=True,
                )
                for event in sorted_events[:5]:
                    timestamp = event.last_timestamp or event.first_timestamp
                    print(f"  {event.type}: {event.reason} - {event.message}")
                    print(f"    Time: {timestamp}")
            else:
                print("  No events found")
        except Exception as e:
            print(f"Failed to list events: {e}")

        print(f"\n{'='*60}")

    except Exception as e:
        print(f"Failed to collect diagnostics: {e}")
