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

import asyncio
import json
import os
import pytest
import requests
import time
from kserve import KServeClient, constants, V1alpha1LLMInferenceService
from kubernetes import client
from kubernetes import watch as k8s_watch
from tabulate import tabulate

KSERVE_TEST_NAMESPACE = "kserve-ci-e2e-test"

KSERVE_GROUP = "serving.kserve.io"
KSERVE_V1ALPHA1_VERSION = "v1alpha1"
KSERVE_PLURAL_LLMINFERENCESERVICE = "llminferenceservices"


@pytest.mark.llminferenceservice
@pytest.mark.asyncio(scope="session") 
async def test_llm_inference_service_facebook_opt():
    service_name = "facebook-opt-125m-single"
    
    llm_isvc = V1alpha1LLMInferenceService(
        api_version="serving.kserve.io/v1alpha1",
        kind="LLMInferenceService",
        metadata=client.V1ObjectMeta(
            name=service_name,
            namespace=KSERVE_TEST_NAMESPACE
        ),
        spec={
            "model": {
                "uri": "hf://facebook/opt-125m",
                "name": "facebook/opt-125m"
            },
            "replicas": 1,
            "router": {
                "scheduler": {},
                "route": {},
                "gateway": {}
            },
            "template": {
                "containers": [{
                    "name": "main",
                    "image": "quay.io/pierdipi/vllm-cpu:latest",
                    "env": [{
                        "name": "VLLM_LOGGING_LEVEL",
                        "value": "DEBUG"
                    }],
                    "resources": {
                        "limits": {"cpu": "2", "memory": "16Gi"},
                        "requests": {"cpu": "1", "memory": "8Gi"}
                    },
                    "livenessProbe": {
                        "initialDelaySeconds": 30,
                        "periodSeconds": 30,
                        "timeoutSeconds": 30,
                        "failureThreshold": 5
                    }
                }]
            }
        }
    )
    
    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )
    
    try:
        create_llmisvc(kserve_client, llm_isvc)
        
        await wait_llm_isvc_ready(kserve_client, service_name, KSERVE_TEST_NAMESPACE)
        
        service_url = get_llm_service_url(kserve_client, service_name, KSERVE_TEST_NAMESPACE)
        
        completion_url = f"{service_url}/v1/completions"
        payload = {
            "model": "facebook/opt-125m",
            "prompt": "San Francisco is a"
        }

        response = requests.post(
            completion_url,
            headers={"Content-Type": "application/json"},
            json=payload,
            timeout=300
        )

        assert response.status_code == 200, f"Expected 200 but got {response.status_code}: {response.text}"
        
        response_data = response.json()
        assert "choices" in response_data, "Response should contain 'choices' field"
        
    finally:
        try:
            print(f"{service_url}/v1/completions")
            delete_llmisvc(kserve_client, service_name, KSERVE_TEST_NAMESPACE)
        except Exception as e:
            print(f"Warning: Failed to cleanup service {service_name}: {e}")

## TODO this can be moved to kserve_client, keeping localized for now

def create_llmisvc(kserve_client, llm_isvc, namespace=None, watch=False, timeout_seconds=600):
    """
    Create LLM inference service - based on KServeClient.create() but for LLMInferenceService
    :param kserve_client: KServe client instance  
    :param llm_isvc: LLM inference service object
    :param namespace: defaults to current or default namespace
    :param watch: True to watch the created service until timeout elapsed or status is ready
    :param timeout_seconds: timeout seconds for watch, default to 600s
    :return: created LLM inference service
    """
    from kserve.utils import utils
    
    version = llm_isvc.api_version.split("/")[1]
    
    if namespace is None:
        namespace = utils.get_isvc_namespace(llm_isvc)
    
    try:
        outputs = kserve_client.api_instance.create_namespaced_custom_object(
            KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICE,
            llm_isvc,
        )
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->create_namespaced_custom_object for LLMInferenceService: {e}"
        )
    
    if watch:
        llm_isvc_watch(
            name=outputs["metadata"]["name"],
            namespace=namespace,
            timeout_seconds=timeout_seconds,
        )
    else:
        return outputs


def delete_llmisvc(kserve_client, name, namespace, version=KSERVE_V1ALPHA1_VERSION):
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
            KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICE,
            name,
        )
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->delete_namespaced_custom_object for LLMInferenceService: {e}"
        )

def get_llmisvc(kserve_client, name, namespace, version=KSERVE_V1ALPHA1_VERSION):
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
            KSERVE_GROUP,
            version,
            namespace,
            KSERVE_PLURAL_LLMINFERENCESERVICE,
            name,
        )
    except client.rest.ApiException as e:
        raise RuntimeError(
            f"Exception when calling CustomObjectsApi->get_namespaced_custom_object for LLMInferenceService: {e}"
        )


async def wait_llm_isvc_ready(
    kserve_client, 
    name, 
    namespace, 
    timeout_seconds=600,
    version=KSERVE_V1ALPHA1_VERSION
):
    """
    Wait for LLM inference service to be ready by checking all conditions
    :param kserve_client: KServe client instance
    :param name: LLM inference service name
    :param namespace: namespace
    :param timeout_seconds: timeout seconds for waiting, default to 600s
    :param version: api group version
    :return: ready LLM inference service object
    """
    llm_isvc_watch(name=name, namespace=namespace, timeout_seconds=timeout_seconds)
    
    llm_isvc = get_llmisvc(kserve_client, name, namespace, version)
    
    return llm_isvc


def get_llm_service_url(kserve_client, service_name, namespace, version=KSERVE_V1ALPHA1_VERSION):
    try:
        llm_isvc = get_llmisvc(kserve_client, service_name, namespace, version)
        
        if "status" not in llm_isvc:
            raise ValueError(f"No status found in LLM inference service {service_name}")
            
        status = llm_isvc["status"]
        
        if "url" in status and status["url"]:
            return status["url"]
        
        if "addresses" in status and status["addresses"] and len(status["addresses"]) > 0:
            first_address = status["addresses"][0]
            if "url" in first_address:
                return first_address["url"]
        
        raise ValueError(f"No URL found in LLM inference service {service_name} status")
        
    except Exception as e:
        raise ValueError(f"Failed to get URL for LLM inference service {service_name}: {e}")
    
def llm_isvc_watch(name=None, namespace=None, timeout_seconds=600, generation=0):
    """
    Watch LLM inference service until all conditions are ready
    """
    headers = ["NAME", "READY", "WORKLOADS", "ROUTER", "MAIN", "URL"]
    table_fmt = "plain"

    stream = k8s_watch.Watch().stream(
        client.CustomObjectsApi().list_namespaced_custom_object,
        KSERVE_GROUP,
        KSERVE_V1ALPHA1_VERSION,
        namespace,
        KSERVE_PLURAL_LLMINFERENCESERVICE,
        timeout_seconds=timeout_seconds,
    )

    for event in stream:
        llm_isvc = event["object"]
        llm_isvc_name = llm_isvc["metadata"]["name"]
        if name and name != llm_isvc_name:
            continue
        else:
            ready = "Unknown"
            workloads_ready = "Unknown"
            router_ready = "Unknown"
            main_workload_ready = "Unknown"
            url = ""
            
            if llm_isvc.get("status", ""):
                status = llm_isvc["status"]
                
                if "url" in status and status["url"]:
                    url = status["url"]
                elif "addresses" in status and status["addresses"]:
                    first_address = status["addresses"][0]
                    if "url" in first_address:
                        url = first_address["url"]
                
                if generation != 0:
                    observed_generation = status.get("observedGeneration")
                    if observed_generation != generation:
                        continue
                
                conditions_status = {}
                for condition in status.get("conditions", []):
                    condition_type = condition.get("type", "")
                    condition_status = condition.get("status", "Unknown")
                    conditions_status[condition_type] = condition_status
                
                ready = conditions_status.get("Ready", "Unknown")
                workloads_ready = conditions_status.get("WorkloadsReady", "Unknown")
                router_ready = conditions_status.get("RouterReady", "Unknown")
                main_workload_ready = conditions_status.get("MainWorkloadReady", "Unknown")
                
                print(
                    tabulate(
                        [[llm_isvc_name, ready, workloads_ready, router_ready, main_workload_ready, url]],
                        headers=headers,
                        tablefmt=table_fmt,
                    )
                )
                
                if (ready == "True" and 
                    workloads_ready == "True" and 
                    router_ready == "True" and
                    main_workload_ready == "True"):
                    print("All conditions ready")
                    break
            else:
                print(
                    tabulate(
                        [[llm_isvc_name, ready, workloads_ready, router_ready, main_workload_ready, ""]],
                        headers=headers,
                        tablefmt=table_fmt,
                    )
                )
                time.sleep(2)
                continue