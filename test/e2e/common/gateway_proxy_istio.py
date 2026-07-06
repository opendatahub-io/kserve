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

"""Istio gateway proxy memory tuning -- pytest plugin.

Activate via ``-p common.gateway_proxy_istio`` when GATEWAY_PROXY_MEMORY
is set.  After each test's ``test_case`` fixture creates router gateways,
this plugin ensures a ConfigMap with an Istio deployment strategic merge
patch exists and patches every Gateway in the test namespace to reference
it via ``parametersRef``.

No upstream files are modified.
"""

import logging
import os

import pytest
import yaml
from kubernetes import client

logger = logging.getLogger(__name__)

GATEWAY_PROXY_MEMORY = os.environ.get("GATEWAY_PROXY_MEMORY")
_RESOURCE_NAME = "gateway-proxy-config"
_ensured_namespaces: set = set()


def _ensure_configmap(namespace, api_client):
    """Create or update the Istio strategic-merge-patch ConfigMap."""
    if namespace in _ensured_namespaces:
        return
    core_api = client.CoreV1Api(api_client)
    patch = {
        "spec": {
            "template": {
                "spec": {
                    "containers": [
                        {
                            "name": "istio-proxy",
                            "resources": {
                                "limits": {"memory": GATEWAY_PROXY_MEMORY},
                                "requests": {"memory": "256Mi"},
                            },
                        }
                    ]
                }
            }
        }
    }
    data = {"deployment": yaml.dump(patch, default_flow_style=False)}
    try:
        existing = core_api.read_namespaced_config_map(_RESOURCE_NAME, namespace)
        existing.data = data
        core_api.replace_namespaced_config_map(_RESOURCE_NAME, namespace, existing)
        logger.info("Updated ConfigMap %s/%s", namespace, _RESOURCE_NAME)
    except client.rest.ApiException as e:
        if e.status == 404:
            body = client.V1ConfigMap(
                metadata=client.V1ObjectMeta(name=_RESOURCE_NAME, namespace=namespace),
                data=data,
            )
            core_api.create_namespaced_config_map(namespace, body)
            logger.info("Created ConfigMap %s/%s", namespace, _RESOURCE_NAME)
        else:
            raise
    _ensured_namespaces.add(namespace)


_PARAMETERS_REF = {
    "group": "",
    "kind": "ConfigMap",
    "name": _RESOURCE_NAME,
}


def _patch_gateways(namespace, api_client):
    """Patch all Gateways in namespace to include parametersRef."""
    custom_api = client.CustomObjectsApi(api_client)
    gateways = custom_api.list_namespaced_custom_object(
        "gateway.networking.k8s.io", "v1", namespace, "gateways"
    )
    for gw in gateways.get("items", []):
        infra = gw.get("spec", {}).get("infrastructure", {})
        if infra.get("parametersRef") == _PARAMETERS_REF:
            continue
        name = gw["metadata"]["name"]
        patch = {
            "spec": {
                "infrastructure": {
                    "parametersRef": _PARAMETERS_REF,
                }
            }
        }
        custom_api.patch_namespaced_custom_object(
            "gateway.networking.k8s.io",
            "v1",
            namespace,
            "gateways",
            name,
            patch,
        )
        logger.info("Patched Gateway %s/%s with parametersRef", namespace, name)


@pytest.fixture(autouse=True)
def ensure_gateway_proxy_memory(test_case):
    """After test_case creates router gateways, patch them for proxy memory."""
    if not GATEWAY_PROXY_MEMORY:
        return

    from kserve import KServeClient

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )
    api_client = kserve_client.api_instance.api_client

    namespace = os.environ.get("KSERVE_TEST_NAMESPACE", "kserve-ci-e2e-test")
    _ensure_configmap(namespace, api_client)
    _patch_gateways(namespace, api_client)
