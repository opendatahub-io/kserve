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

import os
import pytest
import requests
import time
from kserve import KServeClient, constants, V1alpha1LLMInferenceService
from kubernetes import client

# TODO both KServe Gateway and DestinationRule have to exist,
# latter can be created in the test namespace. It should be automated.
KSERVE_TEST_NAMESPACE = (
    "llm-test"  # Aligned with DEV.md - expects to have DestinationRule created
)

KSERVE_PLURAL_LLMINFERENCESERVICE = "llminferenceservices"


@pytest.mark.llminferenceservice
@pytest.mark.asyncio(scope="session")
async def test_llm_inference_service_facebook_opt():
    service_name = "facebook-opt-125m-single"

    llm_isvc = V1alpha1LLMInferenceService(
        api_version="serving.kserve.io/v1alpha1",
        kind="LLMInferenceService",
        metadata=client.V1ObjectMeta(
            name=service_name, namespace=KSERVE_TEST_NAMESPACE
        ),
        spec={
            "model": {"uri": "hf://facebook/opt-125m", "name": "facebook/opt-125m"},
            "replicas": 1,
            "router": {"scheduler": {}, "route": {}, "gateway": {}},
            "template": {
                "containers": [
                    {
                        "name": "main",
                        "image": "quay.io/pierdipi/vllm-cpu:latest",
                        "env": [{"name": "VLLM_LOGGING_LEVEL", "value": "DEBUG"}],
                        "resources": {
                            "limits": {"cpu": "2", "memory": "16Gi"},
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
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )

    try:
        create_llmisvc(kserve_client, llm_isvc)

        wait_for_model_response(kserve_client, service_name, KSERVE_TEST_NAMESPACE)

    finally:
        try:
            delete_llmisvc(kserve_client, service_name, KSERVE_TEST_NAMESPACE)
        except Exception as e:
            print(f"Warning: Failed to cleanup service {service_name}: {e}")


# TODO this can be moved to kserve_client, keeping localized for now


def create_llmisvc(kserve_client, llm_isvc, namespace=None, timeout_seconds=600):
    """
    Create LLM inference service - based on KServeClient.create() but for LLMInferenceService
    :param kserve_client: KServe client instance
    :param llm_isvc: LLM inference service object
    :param namespace: defaults to current or default namespace
    :param timeout_seconds: timeout seconds for create operation, default to 600s
    :return: created LLM inference service
    """
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
            f"Exception when calling CustomObjectsApi->create_namespaced_custom_object for LLMInferenceService: {e}"
        )


def delete_llmisvc(
    kserve_client, name, namespace, version=constants.KSERVE_V1ALPHA1_VERSION
):
    """
    Delete LLM inference service - based on KServeClient.delete() but for LLMInferenceService
    :param kserve_client: KServe client instance
    :param name: LLM inference service name
    :param namespace: namespace
    :param version: api group version
    :return: delete response
    """
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
            f"Exception when calling CustomObjectsApi->delete_namespaced_custom_object for LLMInferenceService: {e}"
        )


def get_llmisvc(
    kserve_client, name, namespace, version=constants.KSERVE_V1ALPHA1_VERSION
):
    """
    Get LLM inference service - based on KServeClient.get() but for LLMInferenceService
    :param kserve_client: KServe client instance
    :param name: LLM inference service name
    :param namespace: namespace
    :param version: api group version
    :return: LLM inference service object
    """
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
            f"Exception when calling CustomObjectsApi->get_namespaced_custom_object for LLMInferenceService: {e}"
        )


def wait_for_model_response(
    kserve_client,
    name,
    namespace,
    timeout_seconds=600,
    version=constants.KSERVE_V1ALPHA1_VERSION,
):
    """
    Wait for LLM model to respond successfully by polling the endpoint
    :param kserve_client: KServe client instance
    :param name: LLM inference service name
    :param namespace: namespace
    :param timeout_seconds: timeout seconds for waiting, default to 600s
    :param version: api group version
    :return: service URL when model responds successfully
    """

    def assert_model_responds():
        service_url = None

        try:
            service_url = get_llm_service_url(kserve_client, name, namespace, version)
        except Exception as e:
            raise AssertionError(f"Failed to get service URL: {e}")

        completion_url = f"{service_url}/v1/completions"
        test_payload = {"model": "facebook/opt-125m", "prompt": "test", "max_tokens": 1}

        try:
            response = requests.post(
                completion_url,
                headers={"Content-Type": "application/json"},
                json=test_payload,
                timeout=30,
            )
        except Exception as e:
            raise AssertionError(f"Failed to call model: {e}")

        assert (
            response.status_code == 200
        ), f"Service returned {response.status_code}: {response.text}"

        response_data = response.json()
        assert "choices" in response_data, "Response should contain 'choices' field"

    wait_for(assert_model_responds, timeout=timeout_seconds, interval=10.0)


def wait_for(assertion_fn, timeout: float = 5.0, interval: float = 0.1):
    """
    Repeatedly calls assertion_fn() until it returns without AssertionError
    or until timeout (in seconds) is reached.
    """
    deadline = time.time() + timeout
    while True:
        try:
            return assertion_fn()
        except AssertionError:
            if time.time() >= deadline:
                # Re-raise the last AssertionError so pytest reports it
                raise
            time.sleep(interval)


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
        )
