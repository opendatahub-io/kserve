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

"""Gateway proxy resource tuning for e2e tests.

When GATEWAY_PROXY_MEMORY is set, creates a gateway-implementation-specific
resource and injects parametersRef into Gateway specs so the proxy gets
the requested memory limits.

Default backend: Envoy Gateway (EnvoyProxy CR).
Alternative backends can be swapped in via set_backend() from a
pytest plugin's pytest_configure hook.
"""

import logging
import os

from kubernetes import client

logger = logging.getLogger(__name__)

GATEWAY_PROXY_MEMORY = os.environ.get("GATEWAY_PROXY_MEMORY")

_RESOURCE_NAME = "gateway-proxy-config"

_ENVOY_PROXY_GROUP = "gateway.envoyproxy.io"
_ENVOY_PROXY_VERSION = "v1alpha1"
_ENVOY_PROXY_PLURAL = "envoyproxies"


class _EnvoyGatewayBackend:

    def ensure(self, namespace, api_client):
        custom_api = client.CustomObjectsApi(api_client)
        body = {
            "apiVersion": f"{_ENVOY_PROXY_GROUP}/{_ENVOY_PROXY_VERSION}",
            "kind": "EnvoyProxy",
            "metadata": {"name": _RESOURCE_NAME, "namespace": namespace},
            "spec": {
                "provider": {
                    "type": "Kubernetes",
                    "kubernetes": {
                        "envoyDeployment": {
                            "container": {
                                "resources": {
                                    "limits": {
                                        "memory": GATEWAY_PROXY_MEMORY,
                                    },
                                    "requests": {"memory": "256Mi"},
                                }
                            }
                        }
                    },
                }
            },
        }
        try:
            custom_api.get_namespaced_custom_object(
                _ENVOY_PROXY_GROUP,
                _ENVOY_PROXY_VERSION,
                namespace,
                _ENVOY_PROXY_PLURAL,
                _RESOURCE_NAME,
            )
            custom_api.replace_namespaced_custom_object(
                _ENVOY_PROXY_GROUP,
                _ENVOY_PROXY_VERSION,
                namespace,
                _ENVOY_PROXY_PLURAL,
                _RESOURCE_NAME,
                body,
            )
            logger.info("Updated EnvoyProxy %s", _RESOURCE_NAME)
        except client.rest.ApiException as e:
            if e.status == 404:
                custom_api.create_namespaced_custom_object(
                    _ENVOY_PROXY_GROUP,
                    _ENVOY_PROXY_VERSION,
                    namespace,
                    _ENVOY_PROXY_PLURAL,
                    body,
                )
                logger.info("Created EnvoyProxy %s", _RESOURCE_NAME)
            else:
                raise

    def parameters_ref(self):
        return {
            "group": _ENVOY_PROXY_GROUP,
            "kind": "EnvoyProxy",
            "name": _RESOURCE_NAME,
        }


_backend = _EnvoyGatewayBackend()


def set_backend(backend):
    """Replace the default backend. Call from pytest_configure in a plugin."""
    global _backend
    _backend = backend


def ensure_proxy_resource(namespace, api_client):
    """Create or update the proxy resource for the active backend."""
    _backend.ensure(namespace, api_client)


def inject_proxy_params(gateway):
    """Inject parametersRef into a gateway spec for proxy memory override."""
    gateway.setdefault("spec", {}).setdefault("infrastructure", {})[
        "parametersRef"
    ] = _backend.parameters_ref()
