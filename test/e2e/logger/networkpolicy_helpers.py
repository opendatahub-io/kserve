# Copyright 2019 The KServe Authors.
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

from kubernetes import client


def allow_logger_ingress(kserve_client, msg_dumper, service_name, namespace):
    # Workaround: loggers are not officially supported in ODH. The event
    # receiver is an ISVC whose workload NetworkPolicy blocks direct log
    # delivery from this test's model, so explicitly allow that traffic here.
    # Revisit this fixture policy when loggers are fully supported in ODH.
    networking_api = client.NetworkingV1Api(
        api_client=kserve_client.core_api.api_client
    )
    networking_api.create_namespaced_network_policy(
        namespace=namespace,
        body={
            "apiVersion": "networking.k8s.io/v1",
            "kind": "NetworkPolicy",
            "metadata": {
                "name": f"{msg_dumper}-allow-logger",
                "namespace": namespace,
            },
            "spec": {
                "podSelector": {
                    "matchLabels": {
                        "serving.kserve.io/inferenceservice": msg_dumper,
                        "component": "predictor",
                    }
                },
                "policyTypes": ["Ingress"],
                "ingress": [
                    {
                        "from": [
                            {
                                "podSelector": {
                                    "matchLabels": {
                                        "serving.kserve.io/inferenceservice": service_name,
                                        "component": "predictor",
                                    }
                                }
                            }
                        ],
                        "ports": [{"protocol": "TCP", "port": 8080}],
                    }
                ],
            },
        },
    )
