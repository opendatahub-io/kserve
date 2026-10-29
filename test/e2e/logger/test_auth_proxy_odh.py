# Copyright 2026 The KServe Authors.
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

"""ODH proxy routing and rollout regressions, independent of a logging backend.

Run against OpenShift with --network-layer=openshift-route. The controller
replacement test additionally requires KSERVE_E2E_REPLACEMENT_CONTROLLER_IMAGE
and KSERVE_E2E_CONTROLLER_NAMESPACE, and must run without other controller tests.
The test restores the original controller image. KSERVE_E2E_PYTHON_IMAGE can
select a mirrored image containing Python 3 and its standard library.
"""

import asyncio
from contextlib import contextmanager, ExitStack
import json
import os
from pathlib import Path
import time
from uuid import uuid4

from kubernetes import client
import pytest

pytestmark = pytest.mark.kserve_on_openshift

GROUP = "serving.kserve.io"
VERSION = "v1beta1"
RUNTIME_VERSION = "v1alpha1"
UPSTREAM_PREFIX = "--upstream=http://localhost:"
# Use one deterministic HTTP responder for the model and independent event sink.
# Receipt metadata verifies delivery and batching without logging request data.
HTTP_SERVER = """
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import sys
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        payload = json.loads(body or '{}')
        request_type = 'org.kubeflow.serving.inference.request'
        is_request_event = (self.headers.get('ce-type') == request_type or
                            payload.get('type') == request_type)
        event = 'inference_request_received' if is_request_event else 'request_received'
        print(json.dumps({'event': event,
                          'instance_count': len(payload.get('instances', []))}), flush=True)
        response = json.dumps({'predictions': [1] * len(payload.get('instances', []))}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(response)))
        self.end_headers()
        self.wfile.write(response)
HTTPServer(('0.0.0.0', int(sys.argv[1])), Handler).serve_forever()
"""


def _wait_for(check, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(1)
    raise AssertionError(f"Condition did not become true within {timeout}s")


def _deployment(kserve_client, name, namespace):
    return kserve_client.app_api.read_namespaced_deployment(
        f"{name}-predictor", namespace
    )


def _pod_logs(kserve_client, name, namespace, container=None):
    # Read raw UTF-8 rather than a client-version-dependent string conversion.
    with kserve_client.core_api.read_namespaced_pod_log(
        name, namespace, container=container, _preload_content=False
    ) as response:
        return response.read().decode("utf-8")


def _bearer_token(kserve_client, name, namespace):
    """Mint a ServiceAccount token allowed to get the InferenceService.

    The kubeconfig may use client certificates, so it cannot be relied on for a bearer token.
    """
    sa_name = f"{name}-client"
    meta = client.V1ObjectMeta(name=sa_name, namespace=namespace)
    rbac = client.RbacAuthorizationV1Api()
    kserve_client.core_api.create_namespaced_service_account(
        namespace, client.V1ServiceAccount(metadata=meta)
    )
    rbac.create_namespaced_role(
        namespace,
        client.V1Role(
            metadata=meta,
            rules=[
                client.V1PolicyRule(
                    api_groups=[GROUP],
                    resources=["inferenceservices"],
                    resource_names=[name],
                    verbs=["get"],
                )
            ],
        ),
    )
    rbac.create_namespaced_role_binding(
        namespace,
        client.V1RoleBinding(
            metadata=meta,
            role_ref=client.V1RoleRef(
                api_group="rbac.authorization.k8s.io", kind="Role", name=sa_name
            ),
            subjects=[
                client.RbacV1Subject(
                    kind="ServiceAccount", name=sa_name, namespace=namespace
                )
            ],
        ),
    )
    response = kserve_client.core_api.create_namespaced_service_account_token(
        sa_name,
        namespace,
        client.AuthenticationV1TokenRequest(
            spec=client.V1TokenRequestSpec(expiration_seconds=3600)
        ),
    )
    return response.status.token


def _wait_ready(kserve_client, name, namespace):
    def ready():
        deployment = _deployment(kserve_client, name, namespace)
        status = deployment.status
        pods = kserve_client.core_api.list_namespaced_pod(
            namespace, label_selector=f"serving.kserve.io/inferenceservice={name}"
        ).items
        return (
            status.observed_generation == deployment.metadata.generation
            and status.updated_replicas == 1
            and status.available_replicas == 1
            and status.replicas == 1
            and len(pods) == 1
            and pods[0].metadata.deletion_timestamp is None
            and bool(pods[0].status.container_statuses)
            and all(item.ready for item in pods[0].status.container_statuses)
        )

    _wait_for(ready, 300)


def _upstream(deployment):
    proxy = next(
        container
        for container in deployment.spec.template.spec.containers
        if container.name in ("kube-rbac-proxy", "oauth-proxy")
    )
    return next(arg for arg in proxy.args if arg.startswith(UPSTREAM_PREFIX))


def _snapshot(kserve_client, name, namespace):
    """Include templates, ReplicaSets, Pod identities, and container restarts."""
    selector = f"serving.kserve.io/inferenceservice={name}"
    deployment = _deployment(kserve_client, name, namespace)
    replicasets = kserve_client.app_api.list_namespaced_replica_set(
        namespace, label_selector=selector
    ).items
    pods = kserve_client.core_api.list_namespaced_pod(
        namespace, label_selector=selector
    ).items
    assert pods, "A preservation assertion requires an existing running Pod"
    return {
        "deployment_uid": deployment.metadata.uid,
        "generation": deployment.metadata.generation,
        "template": deployment.spec.template.to_dict(),
        "replicasets": sorted(replica_set.metadata.uid for replica_set in replicasets),
        "pods": sorted(
            (
                pod.metadata.uid,
                sorted(
                    (status.name, status.restart_count)
                    for status in pod.status.container_statuses or []
                ),
            )
            for pod in pods
        ),
    }


def _assert_preserved(kserve_client, workloads, namespace, duration=30):
    deadline = time.monotonic() + duration
    while time.monotonic() < deadline:
        for name, before in workloads.items():
            assert _snapshot(kserve_client, name, namespace) == before, name
        time.sleep(1)


@contextmanager
def _workload(kserve_client, namespace, logger=False, stale=False, paused=False):
    """Create an isolated runtime, authenticated predictor, and event sink."""
    name = f"proxy-routing-{uuid4().hex[:8]}"
    image = os.getenv("KSERVE_E2E_PYTHON_IMAGE", "python:3.11-slim-bookworm")
    container = {
        "name": "kserve-container",
        "image": image,
        "command": ["python3", "-u", "-c", HTTP_SERVER],
        "args": ["8080"],
        "ports": [{"containerPort": 8080}],
        "readinessProbe": {"httpGet": {"path": "/", "port": 8080}},
        "resources": {"requests": {"cpu": "10m", "memory": "64Mi"}},
        "securityContext": {
            "allowPrivilegeEscalation": False,
            "capabilities": {"drop": ["ALL"]},
            "runAsNonRoot": True,
            "seccompProfile": {"type": "RuntimeDefault"},
        },
    }
    runtime = {
        "apiVersion": f"{GROUP}/{RUNTIME_VERSION}",
        "kind": "ServingRuntime",
        "metadata": {"name": name},
        "spec": {
            "supportedModelFormats": [{"name": "proxy-test"}],
            "containers": [container],
        },
    }
    sink_labels = {"app": f"{name}-sink"}
    sink = {
        "metadata": {"name": f"{name}-sink"},
        "spec": {
            "replicas": 1,
            "selector": {"matchLabels": sink_labels},
            "template": {
                "metadata": {"labels": sink_labels},
                "spec": {"containers": [container]},
            },
        },
    }
    api = kserve_client.api_instance
    # Register cleanup immediately after each successful create, including setup failures.
    with ExitStack() as cleanup:
        kserve_client.core_api.create_namespaced_service_account(
            namespace, {"metadata": {"name": name}}
        )
        cleanup.callback(
            kserve_client.core_api.delete_namespaced_service_account, name, namespace
        )
        # A PVC selects single-model serving without downloading an external model.
        # The deterministic responder does not need any model files.
        kserve_client.core_api.create_namespaced_persistent_volume_claim(
            namespace,
            {
                "metadata": {"name": name},
                "spec": {
                    "accessModes": ["ReadWriteOnce"],
                    "resources": {"requests": {"storage": "1Gi"}},
                },
            },
        )
        cleanup.callback(
            kserve_client.core_api.delete_namespaced_persistent_volume_claim,
            name,
            namespace,
        )
        api.create_namespaced_custom_object(
            GROUP, RUNTIME_VERSION, namespace, "servingruntimes", runtime
        )
        cleanup.callback(
            api.delete_namespaced_custom_object,
            GROUP,
            RUNTIME_VERSION,
            namespace,
            "servingruntimes",
            name,
        )
        kserve_client.app_api.create_namespaced_deployment(namespace, sink)
        cleanup.callback(
            kserve_client.app_api.delete_namespaced_deployment,
            f"{name}-sink",
            namespace,
        )
        kserve_client.core_api.create_namespaced_service(
            namespace,
            {
                "metadata": {"name": f"{name}-sink"},
                "spec": {
                    "selector": sink_labels,
                    "ports": [{"port": 80, "targetPort": 8080}],
                },
            },
        )
        cleanup.callback(
            kserve_client.core_api.delete_namespaced_service,
            f"{name}-sink",
            namespace,
        )
        _wait_for(
            lambda: (
                kserve_client.app_api.read_namespaced_deployment(
                    f"{name}-sink", namespace
                ).status.available_replicas
                == 1
            )
        )
        # Keep successive predictor revisions on one node so the RWO model
        # volume can be shared during a rolling update.
        sink_pod = kserve_client.core_api.list_namespaced_pod(
            namespace, label_selector=f"app={name}-sink"
        ).items[0]
        node = kserve_client.core_api.read_node(sink_pod.spec.node_name)
        api.patch_namespaced_custom_object(
            GROUP,
            RUNTIME_VERSION,
            namespace,
            "servingruntimes",
            name,
            {
                "spec": {
                    "nodeSelector": {
                        "kubernetes.io/hostname": node.metadata.labels[
                            "kubernetes.io/hostname"
                        ]
                    }
                }
            },
        )
        predictor = {
            "minReplicas": 1,
            "maxReplicas": 1,
            "serviceAccountName": name,
            "model": {
                "modelFormat": {"name": "proxy-test"},
                "runtime": name,
                "storageUri": f"pvc://{name}",
            },
        }
        logger_spec = {"mode": "all", "url": f"http://{name}-sink.{namespace}.svc"}
        if logger:
            predictor["logger"] = logger_spec
        api.create_namespaced_custom_object(
            GROUP,
            VERSION,
            namespace,
            "inferenceservices",
            {
                "apiVersion": f"{GROUP}/{VERSION}",
                "kind": "InferenceService",
                "metadata": {
                    "name": name,
                    "annotations": {
                        "serving.kserve.io/deploymentMode": "Standard",
                        "serving.kserve.io/autoscalerClass": "external",
                        "security.opendatahub.io/enable-auth": "true",
                        "serving.kserve.io/disable-auto-update": str(paused).lower(),
                    },
                    "labels": {"networking.kserve.io/visibility": "exposed"},
                },
                "spec": {"predictor": predictor},
            },
        )
        cleanup.callback(
            api.delete_namespaced_custom_object,
            GROUP,
            VERSION,
            namespace,
            "inferenceservices",
            name,
        )
        kserve_client.wait_isvc_ready(name, namespace=namespace, timeout_seconds=300)
        _wait_ready(kserve_client, name, namespace)
        if stale:
            assert logger
            deployment = _deployment(kserve_client, name, namespace)
            proxy = next(
                container
                for container in deployment.spec.template.spec.containers
                if container.name in ("kube-rbac-proxy", "oauth-proxy")
            )
            proxy.args = [
                f"{UPSTREAM_PREFIX}8080" if arg.startswith(UPSTREAM_PREFIX) else arg
                for arg in proxy.args
            ]
            kserve_client.app_api.patch_namespaced_deployment(
                deployment.metadata.name,
                namespace,
                {
                    "spec": {
                        "template": {
                            "spec": {
                                "containers": [{"name": proxy.name, "args": proxy.args}]
                            }
                        }
                    }
                },
            )
            _wait_ready(kserve_client, name, namespace)
        expected_port = 8080 if stale or not logger else 9081
        assert (
            _upstream(_deployment(kserve_client, name, namespace))
            == f"{UPSTREAM_PREFIX}{expected_port}"
        )
        yield name, logger_spec


@pytest.mark.parametrize("feature", ["logger", "batcher"])
@pytest.mark.asyncio(scope="session")
async def test_late_agent_activation_routes_through_agent(
    kserve_client, rest_v1_client, test_namespace, feature
):
    """Late activation updates the proxy and performs logging or batching."""
    with _workload(kserve_client, test_namespace) as (name, logger_spec):
        assert (
            _upstream(_deployment(kserve_client, name, test_namespace))
            == f"{UPSTREAM_PREFIX}8080"
        )
        kserve_client.api_instance.patch_namespaced_custom_object(
            GROUP,
            VERSION,
            test_namespace,
            "inferenceservices",
            name,
            {
                "spec": {
                    "predictor": {
                        feature: logger_spec
                        if feature == "logger"
                        else {"maxBatchSize": 32, "maxLatency": 5000}
                    }
                }
            },
        )
        _wait_for(
            lambda: (
                _upstream(_deployment(kserve_client, name, test_namespace))
                == f"{UPSTREAM_PREFIX}9081"
            )
        )
        _wait_ready(kserve_client, name, test_namespace)
        token = _bearer_token(kserve_client, name, test_namespace)
        service = kserve_client.get(name, namespace=test_namespace)
        filename = (
            "iris_batch_input.json" if feature == "batcher" else "iris_input.json"
        )
        payload = json.loads((Path("data") / filename).read_text())
        requests = payload if feature == "batcher" else [payload]
        # Use the SDK directly: the generic prediction helper logs its headers.
        results = await asyncio.gather(
            *(
                rest_v1_client.infer(
                    service["status"]["url"],
                    request,
                    model_name=name,
                    headers={"Authorization": f"Bearer {token}"},
                )
                for request in requests
            )
        )
        assert len(results) == len(requests)
        assert all(result.get("predictions") == [1, 1] for result in results), results
        if feature == "batcher":
            # The agent adds batch metadata to the model's prediction response.
            assert all(result.get("batchId") for result in results), results
            assert all(result.get("message") == "" for result in results), results
            pods = kserve_client.core_api.list_namespaced_pod(
                test_namespace,
                label_selector=f"serving.kserve.io/inferenceservice={name}",
            ).items
            logs = _pod_logs(
                kserve_client,
                pods[0].metadata.name,
                test_namespace,
                container="kserve-container",
            )
            batches = [
                json.loads(line) for line in logs.splitlines() if line.startswith("{")
            ]
            assert any(batch.get("instance_count", 0) > 2 for batch in batches), logs
            return

        def sink_received_request():
            pods = kserve_client.core_api.list_namespaced_pod(
                test_namespace, label_selector=f"app={name}-sink"
            ).items
            return any(
                '"event": "inference_request_received"'
                in _pod_logs(kserve_client, pod.metadata.name, test_namespace)
                for pod in pods
            )

        await asyncio.to_thread(_wait_for, sink_received_request, 60)


def _set_runtime_port(kserve_client, name, namespace, port):
    runtime = kserve_client.api_instance.get_namespaced_custom_object(
        GROUP, RUNTIME_VERSION, namespace, "servingruntimes", name
    )
    container = runtime["spec"]["containers"][0]
    container["ports"][0]["containerPort"] = port
    container["args"] = [str(port)]
    container["readinessProbe"]["httpGet"]["port"] = port
    kserve_client.api_instance.replace_namespaced_custom_object(
        GROUP, RUNTIME_VERSION, namespace, "servingruntimes", name, runtime
    )


@pytest.mark.parametrize("logger", [False, True], ids=["direct-model", "logger-agent"])
def test_runtime_port_change_preserves_paused_workload(
    kserve_client, test_namespace, logger
):
    """A runtime port edit must not roll a Ready workload with auto-update disabled."""
    with _workload(kserve_client, test_namespace, logger=logger, paused=True) as (
        name,
        _,
    ):
        before = _snapshot(kserve_client, name, test_namespace)
        _set_runtime_port(kserve_client, name, test_namespace, 8081)
        _assert_preserved(kserve_client, {name: before}, test_namespace)


@pytest.mark.asyncio(scope="session")
@pytest.mark.parametrize("logger", [False, True], ids=["direct-model", "stale-logger"])
async def test_runtime_port_change_keeps_authenticated_inference_working(
    kserve_client, rest_v1_client, test_namespace, logger
):
    """An allowed model-port update keeps direct and stale Logger routes usable."""
    with _workload(kserve_client, test_namespace, logger=logger, stale=logger) as (
        name,
        _,
    ):
        before = _snapshot(kserve_client, name, test_namespace)
        _set_runtime_port(kserve_client, name, test_namespace, 8081)
        expected_port = 9081 if logger else 8081
        _wait_for(
            lambda: _upstream(_deployment(kserve_client, name, test_namespace))
            == f"{UPSTREAM_PREFIX}{expected_port}"
        )
        _wait_ready(kserve_client, name, test_namespace)
        after = _snapshot(kserve_client, name, test_namespace)
        assert after["deployment_uid"] == before["deployment_uid"]
        assert after["pods"] != before["pods"], "The model-port edit requires new Pods"
        service = kserve_client.get(name, namespace=test_namespace)
        token = _bearer_token(kserve_client, name, test_namespace)
        result = await rest_v1_client.infer(
            service["status"]["url"],
            json.loads((Path("data") / "iris_input.json").read_text()),
            model_name=name,
            headers={"Authorization": f"Bearer {token}"},
        )
        assert result.get("predictions") == [1, 1], result


def test_controller_replacement_preserves_existing_workloads(
    kserve_client, test_namespace
):
    """Replacing the controller preserves correct and stale existing proxy routes."""
    image = os.getenv("KSERVE_E2E_REPLACEMENT_CONTROLLER_IMAGE")
    namespace = os.getenv("KSERVE_E2E_CONTROLLER_NAMESPACE")
    if not image or not namespace:
        pytest.skip("Requires an explicit replacement controller image and namespace")
    deployment_name = "kserve-controller-manager"
    deployment = kserve_client.app_api.read_namespaced_deployment(
        deployment_name, namespace
    )
    manager = next(
        container
        for container in deployment.spec.template.spec.containers
        if container.name == "manager"
    )
    original_image = manager.image
    assert original_image != image, "Controller replacement must use a different image"
    with ExitStack() as workloads:
        names = [
            workloads.enter_context(
                _workload(kserve_client, test_namespace, logger=logger, stale=stale)
            )[0]
            for logger, stale in ((False, False), (True, False), (True, True))
        ]
        before = {
            name: _snapshot(kserve_client, name, test_namespace) for name in names
        }

        def replace_controller(replacement):
            kserve_client.app_api.patch_namespaced_deployment(
                deployment_name,
                namespace,
                {
                    "spec": {
                        "template": {
                            "spec": {
                                "containers": [
                                    {"name": "manager", "image": replacement}
                                ]
                            }
                        }
                    }
                },
            )

            def rolled_out():
                current = kserve_client.app_api.read_namespaced_deployment(
                    deployment_name, namespace
                )
                return (
                    next(
                        container.image
                        for container in current.spec.template.spec.containers
                        if container.name == "manager"
                    )
                    == replacement
                    and current.status.observed_generation
                    == current.metadata.generation
                    and current.status.replicas == current.spec.replicas
                    and current.status.updated_replicas == current.spec.replicas
                    and current.status.available_replicas == current.spec.replicas
                )

            _wait_for(rolled_out, 300)

        try:
            replace_controller(image)
            _assert_preserved(kserve_client, before, test_namespace, duration=60)
        finally:
            replace_controller(original_image)
