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

"""Istio backend for gateway proxy resource tuning.

Pytest plugin -- activate via ``-p common.gateway_proxy_istio``.
Creates a ConfigMap with an Istio deployment strategic merge patch
and points parametersRef at it.
"""

import logging

import yaml
from kubernetes import client

from .gateway_proxy import (
    GATEWAY_PROXY_MEMORY,
    _RESOURCE_NAME,
    set_backend,
)

logger = logging.getLogger(__name__)


class _IstioBackend:

    def ensure(self, namespace, api_client):
        core_api = client.CoreV1Api(api_client)
        patch = {
            "spec": {
                "template": {
                    "spec": {
                        "containers": [
                            {
                                "name": "istio-proxy",
                                "resources": {
                                    "limits": {
                                        "memory": GATEWAY_PROXY_MEMORY,
                                    },
                                    "requests": {"memory": "256Mi"},
                                },
                            }
                        ]
                    }
                }
            }
        }
        data = {
            "deployment": yaml.dump(patch, default_flow_style=False),
        }
        try:
            existing = core_api.read_namespaced_config_map(
                _RESOURCE_NAME, namespace
            )
            existing.data = data
            core_api.replace_namespaced_config_map(
                _RESOURCE_NAME, namespace, existing
            )
            logger.info("Updated ConfigMap %s", _RESOURCE_NAME)
        except client.rest.ApiException as e:
            if e.status == 404:
                body = client.V1ConfigMap(
                    metadata=client.V1ObjectMeta(
                        name=_RESOURCE_NAME,
                        namespace=namespace,
                    ),
                    data=data,
                )
                core_api.create_namespaced_config_map(namespace, body)
                logger.info("Created ConfigMap %s", _RESOURCE_NAME)
            else:
                raise

    def parameters_ref(self):
        return {
            "group": "",
            "kind": "ConfigMap",
            "name": _RESOURCE_NAME,
        }


def pytest_configure(config):
    set_backend(_IstioBackend())
