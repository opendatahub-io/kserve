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
            "gatewayClassName": "istio",
            "listeners": {
                {
                    "name": "http",
                    "port": 80,
                    "protocol": "http",
                    "allowedRoutes": {
                        "namespaces": {
                            "from": "All",
                        },
                    },
                },
            },
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
            "gatewayClassName": "istio",
            "listeners": {
                {
                    "name": "http",
                    "port": 80,
                    "protocol": "http",
                    "allowedRoutes": {
                        "namespaces": {
                            "from": "All",
                        },
                    },
                },
            },
        },
    }
]

ROUTER_ROUTES = [
    {
        "apiVersion": "gateway.networking.k8s.io/v1",
        "kind": "HTTPRoute",
        "metadata": {
            "name": "router-route-1",
            "namespace": KSERVE_TEST_NAMESPACE,
        },
        "spec": {
            "parentRefs": [
                {
                    "name": "router-gateway-1",
                    "namespace": KSERVE_TEST_NAMESPACE,
                }
            ],
            "rules": [
                {
                    "matches": [
                        {
                            "path": {
                                "type": "Exact",
                                "value": "/v1/completions",
                            },
                        },
                    ],
                    "backendRefs": [
                        {
                            "group": "",
                            "kind": "Service",
                            "name": "router-references-test-kserve-workload-svc",
                            "namespace": KSERVE_TEST_NAMESPACE,
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
            "name": "router-route-2",
            "namespace": KSERVE_TEST_NAMESPACE,
        },
        "spec": {
            "parentRefs": [
                {
                    "name": "router-gateway-1",
                    "namespace": KSERVE_TEST_NAMESPACE,
                }
            ],
            "rules": [
                {
                    "matches": [
                        {
                            "path": {
                                "type": "Exact",
                                "value": "/v1/chat/completions",
                            },
                        },
                    ],
                    "backendRefs": [
                        {
                            "group": "",
                            "kind": "Service",
                            "name": "router-references-test-kserve-workload-svc",
                            "namespace": KSERVE_TEST_NAMESPACE,
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
            "name": "router-route-3",
            "namespace": KSERVE_TEST_NAMESPACE,
        },
        "spec": {
            "parentRefs": [
                {
                    "name": "router-gateway-2",
                    "namespace": KSERVE_TEST_NAMESPACE,
                }
            ],
            "rules": [
                {
                    "matches": [
                        {
                            "path": {
                                "type": "Exact",
                                "value": "/v1/completions",
                            },
                        },
                    ],
                    "backendRefs": [
                        {
                            "group": "",
                            "kind": "Service",
                            "name": "router-references-pd-test-kserve-workload-svc",
                            "namespace": KSERVE_TEST_NAMESPACE,
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
            "name": "router-route-4",
            "namespace": KSERVE_TEST_NAMESPACE,
        },
        "spec": {
            "parentRefs": [
                {
                    "name": "router-gateway-2",
                    "namespace": KSERVE_TEST_NAMESPACE,
                }
            ],
            "rules": [
                {
                    "matches": [
                        {
                            "path": {
                                "type": "Exact",
                                "value": "/v1/chat/completions",
                            },
                        },
                    ],
                    "backendRefs": [
                        {
                            "group": "",
                            "kind": "Service",
                            "name": "router-references-pd-test-kserve-workload-svc",
                            "namespace": KSERVE_TEST_NAMESPACE,
                        }
                    ],
                },
            ],
        },
    }
]
