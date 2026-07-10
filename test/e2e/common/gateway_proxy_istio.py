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
is set.  Ensures a ConfigMap with an Istio deployment strategic merge
patch exists and patches every Gateway in the test namespace to reference
it via ``parametersRef``, then waits for gateways to become Programmed.

No upstream files are modified.
"""

import logging
import os
import time

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
            try:
                core_api.create_namespaced_config_map(namespace, body)
                logger.info("Created ConfigMap %s/%s", namespace, _RESOURCE_NAME)
            except client.rest.ApiException as create_err:
                if create_err.status != 409:
                    raise
        else:
            raise
    _ensured_namespaces.add(namespace)


_PARAMETERS_REF = {
    "group": "",
    "kind": "ConfigMap",
    "name": _RESOURCE_NAME,
}


def _patch_gateways(namespace, api_client):
    """Patch all Gateways in namespace to include parametersRef.

    Returns the list of gateway names that were actually patched.
    """
    custom_api = client.CustomObjectsApi(api_client)
    gateways = custom_api.list_namespaced_custom_object(
        "gateway.networking.k8s.io", "v1", namespace, "gateways"
    )
    patched = []
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
        patched.append(name)
    return patched


def _wait_for_gateways_programmed(namespace, api_client, names, timeout=120):
    """Wait until all named Gateways have Programmed=True."""
    custom_api = client.CustomObjectsApi(api_client)
    deadline = time.monotonic() + timeout
    pending = set(names)
    while pending and time.monotonic() < deadline:
        for name in list(pending):
            gw = custom_api.get_namespaced_custom_object(
                "gateway.networking.k8s.io", "v1", namespace, "gateways", name
            )
            conditions = gw.get("status", {}).get("conditions", [])
            for c in conditions:
                if c.get("type") == "Programmed" and c.get("status") == "True":
                    logger.info("Gateway %s/%s is Programmed", namespace, name)
                    pending.discard(name)
                    break
        if pending:
            time.sleep(2)
    if pending:
        logger.warning(
            "Gateways not Programmed after %ds: %s", timeout, sorted(pending)
        )


@pytest.fixture(autouse=True)
def ensure_gateway_proxy_memory(request):
    """After test setup creates gateways, patch them for proxy memory."""
    if not GATEWAY_PROXY_MEMORY:
        return

    # Establish ordering: if the test uses test_case (llmisvc), let it run first
    if "test_case" in request.fixturenames:
        request.getfixturevalue("test_case")

    from kserve import KServeClient

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )
    api_client = kserve_client.api_instance.api_client

    namespace = os.environ.get("KSERVE_TEST_NAMESPACE", "kserve-ci-e2e-test")
    _ensure_configmap(namespace, api_client)
    patched = _patch_gateways(namespace, api_client)
    if patched:
        _wait_for_gateways_programmed(namespace, api_client, patched)
