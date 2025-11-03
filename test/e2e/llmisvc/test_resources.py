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

from .fixtures import KSERVE_TEST_NAMESPACE

ROUTER_GATEWAYS = [
    {
        "apiVersion": "gateway.networking.k8s.io/v1",
        "kind": "Gateway",
        "metadata": {
            "name": "router-gateway-1",
            "namespace": KSERVE_TEST_NAMESPACE,
        },
        "spec": {
            "gatewayClassName": "openshift-default",
            "listeners": [
                {
                    "name": "http",
                    "port": 80,
                    "protocol": "HTTP",
                    "allowedRoutes": {
                        "namespaces": {
                            "from": "All",
                        },
                    },
                },
            ],
        },
    },
    {
        "apiVersion": "gateway.networking.k8s.io/v1",
        "kind": "Gateway",
        "metadata": {
            "name": "router-gateway-2",
            "namespace": KSERVE_TEST_NAMESPACE,
        },
        "spec": {
            "gatewayClassName": "openshift-default",
            "listeners": [
                {
                    "name": "http",
                    "port": 80,
                    "protocol": "HTTP",
                    "allowedRoutes": {
                        "namespaces": {
                            "from": "All",
                        },
                    },
                },
            ],
        },
    }
]


def get_router_routes(service_name: str, gateway_name: str = "router-gateway-1"):
    """
    Generate HTTPRoute resources for a given service name.

    Args:
        service_name: The service name with API version (e.g., "router-with-refs-test-v1alpha2")
        gateway_name: The gateway to attach routes to (default: "router-gateway-1")

    Returns:
        List of HTTPRoute resources with service-specific names
    """
    # Extract base name without version for path matching
    # e.g., "router-with-refs-test-v1alpha2" -> "router-with-refs-test"
    # This assumes service names end with "-v1alpha1" or "-v1alpha2"
    if service_name.endswith("-v1alpha1") or service_name.endswith("-v1alpha2"):
        base_name = service_name.rsplit("-", 1)[0]
    else:
        base_name = service_name

    inference_pool_name = f"{service_name}-inference-pool"
    workload_service_name = f"{service_name}-kserve-workload-svc"

    return [
        {
            "apiVersion": "gateway.networking.k8s.io/v1",
            "kind": "HTTPRoute",
            "metadata": {
                "name": f"{service_name}-route-1",
                "namespace": KSERVE_TEST_NAMESPACE,
            },
            "spec": {
                "parentRefs": [
                    {
                        "name": gateway_name,
                        "namespace": KSERVE_TEST_NAMESPACE,
                    }
                ],
                "rules": [
                    {
                        "matches": [
                            {
                                "path": {
                                    "type": "PathPrefix",
                                    "value": f"/kserve-ci-e2e-test/{base_name}/v1/completions",
                                },
                            },
                        ],
                        "filters": [
                            {
                                "type": "URLRewrite",
                                "urlRewrite": {
                                    "path": {
                                        "replacePrefixMatch": "/v1/completions",
                                        "type": "ReplacePrefixMatch",
                                    },
                                },
                            },
                        ],
                        "backendRefs": [
                            {
                                "group": "inference.networking.x-k8s.io",
                                "kind": "InferencePool",
                                "name": inference_pool_name,
                                "namespace": KSERVE_TEST_NAMESPACE,
                                "port": 8000,
                            }
                        ],
                    },
                    {
                        "matches": [
                            {
                                "path": {
                                    "type": "PathPrefix",
                                    "value": f"/kserve-ci-e2e-test/{base_name}/v1/chat/completions",
                                },
                            },
                        ],
                        "filters": [
                            {
                                "type": "URLRewrite",
                                "urlRewrite": {
                                    "path": {
                                        "replacePrefixMatch": "/v1/chat/completions",
                                        "type": "ReplacePrefixMatch",
                                    },
                                },
                            },
                        ],
                        "backendRefs": [
                            {
                                "group": "inference.networking.x-k8s.io",
                                "kind": "InferencePool",
                                "name": inference_pool_name,
                                "namespace": KSERVE_TEST_NAMESPACE,
                                "port": 8000,
                            }
                        ],
                    },
                    {
                        "matches": [
                            {
                                "path": {
                                    "type": "PathPrefix",
                                    "value": f"/kserve-ci-e2e-test/{base_name}/v1/models",
                                },
                            },
                        ],
                        "filters": [
                            {
                                "type": "URLRewrite",
                                "urlRewrite": {
                                    "path": {
                                        "replacePrefixMatch": "/v1/models",
                                        "type": "ReplacePrefixMatch",
                                    },
                                },
                            },
                        ],
                        "backendRefs": [
                            {
                                "group": "",
                                "kind": "Service",
                                "name": workload_service_name,
                                "namespace": KSERVE_TEST_NAMESPACE,
                                "port": 8000,
                            }
                        ],
                    },
                    {
                        "matches": [
                            {
                                "path": {
                                    "type": "PathPrefix",
                                    "value": f"/kserve-ci-e2e-test/{base_name}",
                                },
                            },
                        ],
                        "filters": [
                            {
                                "type": "URLRewrite",
                                "urlRewrite": {
                                    "path": {
                                        "replacePrefixMatch": "/",
                                        "type": "ReplacePrefixMatch",
                                    },
                                },
                            },
                        ],
                        "backendRefs": [
                            {
                                "group": "",
                                "kind": "Service",
                                "name": workload_service_name,
                                "namespace": KSERVE_TEST_NAMESPACE,
                                "port": 8000,
                            }
                        ],
                    },
                ],
            },
        },
        {
            "apiVersion": "gateway.networking.k8s.io/v1",
            "kind": "HTTPRoute",
            "metadata": {
                "name": f"{service_name}-route-2",
                "namespace": KSERVE_TEST_NAMESPACE,
            },
            "spec": {
                "parentRefs": [
                    {
                        "name": gateway_name,
                        "namespace": KSERVE_TEST_NAMESPACE,
                    }
                ],
                "rules": [
                    {
                        "matches": [
                            {
                                "path": {
                                    "type": "PathPrefix",
                                    "value": f"/kserve-ci-e2e-test/{base_name}/v1/organization/usage/completions",
                                },
                            },
                        ],
                        "filters": [
                            {
                                "type": "URLRewrite",
                                "urlRewrite": {
                                    "path": {
                                        "replacePrefixMatch": "/v1/organization/usage/completions",
                                        "type": "ReplacePrefixMatch",
                                    },
                                },
                            },
                        ],
                        "backendRefs": [
                            {
                                "group": "",
                                "kind": "Service",
                                "name": workload_service_name,
                                "namespace": KSERVE_TEST_NAMESPACE,
                                "port": 8000,
                            }
                        ],
                    },
                ],
            },
        },
    ]


# Legacy static routes list - kept for backward compatibility
# New code should use get_router_routes() instead
ROUTER_ROUTES = []
