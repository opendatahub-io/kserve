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

"""Istio gateway proxy memory tuning and diagnostics -- pytest plugin.

Activate via ``-p common.gateway_proxy_istio`` when GATEWAY_PROXY_MEMORY
is set.  Two fixtures:

1. **ensure_gateway_proxy_memory** (per-test, autouse) -- ensures a ConfigMap
   with an Istio deployment strategic merge patch exists and patches every
   Gateway in the test namespace to reference it via ``parametersRef``, then
   waits for gateways to become Programmed.

2. **gateway_diagnostics_monitor** (session, autouse) -- background thread
   that periodically snapshots Envoy memory/stats, gateway pod restarts,
   resource counts, and node metrics to JSONL.  Only active when
   ``--network-layer=gateway-api``.

No upstream files are modified.
"""

import json
import logging
import os
import subprocess
import threading
import time

import pytest
import yaml
from kubernetes import client

logger = logging.getLogger(__name__)

GATEWAY_PROXY_MEMORY = os.environ.get("GATEWAY_PROXY_MEMORY")
_RESOURCE_NAME = "gateway-proxy-config"
_ensured_namespaces: set = set()

# ---------------------------------------------------------------------------
# Proxy memory tuning
# ---------------------------------------------------------------------------


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
        existing = core_api.read_namespaced_config_map(
            _RESOURCE_NAME, namespace
        )
        existing.data = data
        core_api.replace_namespaced_config_map(
            _RESOURCE_NAME, namespace, existing
        )
        logger.info(
            "Updated ConfigMap %s/%s", namespace, _RESOURCE_NAME
        )
    except client.rest.ApiException as e:
        if e.status == 404:
            body = client.V1ConfigMap(
                metadata=client.V1ObjectMeta(
                    name=_RESOURCE_NAME, namespace=namespace
                ),
                data=data,
            )
            try:
                core_api.create_namespaced_config_map(
                    namespace, body
                )
                logger.info(
                    "Created ConfigMap %s/%s",
                    namespace,
                    _RESOURCE_NAME,
                )
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
        logger.info(
            "Patched Gateway %s/%s with parametersRef",
            namespace,
            name,
        )
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

    # Let test_case (llmisvc) create gateways first

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


# ---------------------------------------------------------------------------
# Diagnostics monitoring
# ---------------------------------------------------------------------------

_TEST_NAMESPACE = os.environ.get("KSERVE_TEST_NAMESPACE", "kserve-ci-e2e-test")

_GATEWAY_PODS = [
    (_TEST_NAMESPACE, "router-gateway-1-openshift-default"),
    (_TEST_NAMESPACE, "router-gateway-2-openshift-default"),
    ("openshift-ingress", "openshift-ai-inference-openshift-default"),
]

_ENVOY_ENDPOINTS = [
    ("memory", "/memory"),
    ("cluster_stats", "/stats?filter=cluster_manager"),
    ("server_memory", "/stats?filter=server.memory"),
    ("wasm_stats", "/stats?usedonly&filter=wasm"),
]

_SNAPSHOT_INTERVAL = int(os.environ.get("GATEWAY_DIAG_INTERVAL", "300"))


def _run_cli(cli, args, timeout=15):
    """Run a CLI command and return stdout, or None on failure."""
    try:
        result = subprocess.run(
            [cli] + args,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
        if result.returncode != 0:
            return None
        return result.stdout.strip()
    except subprocess.TimeoutExpired:
        logger.debug(
            "CLI timeout after %ds: %s %s",
            timeout, cli, " ".join(args[:3]),
        )
        return None
    except Exception as e:
        logger.debug(
            "CLI failed (%s): %s %s",
            type(e).__name__, cli, " ".join(args[:3]),
        )
        return None


def _find_running_pod(cli, namespace, name_prefix):
    """Find a running pod whose name starts with name_prefix."""
    out = _run_cli(
        cli,
        [
            "get",
            "pods",
            "-n",
            namespace,
            "--field-selector=status.phase=Running",
            "-o",
            "jsonpath={.items[*].metadata.name}",
        ],
    )
    if not out:
        return None
    for name in out.split():
        if name.startswith(name_prefix):
            return name
    return None


def _exec_in_pod(cli, namespace, pod_name, endpoint):
    """Hit an Envoy admin endpoint inside a pod's istio-proxy container."""
    try:
        result = subprocess.run(
            [
                cli,
                "exec",
                "-n",
                namespace,
                pod_name,
                "-c",
                "istio-proxy",
                "--",
                "pilot-agent",
                "request",
                "GET",
                endpoint,
            ],
            capture_output=True,
            text=True,
            timeout=15,
        )
        if result.returncode != 0:
            return f"error: exit {result.returncode}"
        return result.stdout.strip()
    except subprocess.TimeoutExpired:
        return "error: timeout"
    except Exception as e:
        return f"error: {e}"


def _get_gateway_restarts(cli):
    """Get restart counts for all gateway pods."""
    restarts = {}
    for namespace, prefix in _GATEWAY_PODS:
        pod_key = f"{namespace}/{prefix}"
        out = _run_cli(
            cli,
            [
                "get",
                "pods",
                "-n",
                namespace,
                "--field-selector=status.phase=Running",
                "-o",
                "jsonpath={range .items[*]}"
                "{.metadata.name}{'\\t'}"
                "{range .status.containerStatuses[*]}"
                "{.restartCount}{'\\t'}{.state}"
                "{end}{'\\n'}{end}",
            ],
        )
        if not out:
            restarts[pod_key] = "error"
            continue
        for line in out.strip().split("\n"):
            if not line:
                continue
            parts = line.split("\t", 1)
            name = parts[0]
            if name.startswith(prefix):
                restarts[pod_key] = parts[1] if len(parts) > 1 else "?"
                break
    return restarts


def _get_resource_counts(cli):
    """Count HTTPRoutes, LLMISvcs, and AuthPolicies in test namespace."""
    counts = {}
    resources = [
        ("httproutes", "gateway.networking.k8s.io"),
        ("llminferenceservices", "serving.kserve.io"),
        ("authpolicies", "kuadrant.io"),
    ]
    for resource, group in resources:
        out = _run_cli(
            cli,
            [
                "get",
                f"{resource}.{group}",
                "-n",
                _TEST_NAMESPACE,
                "--no-headers",
                "--ignore-not-found",
            ],
        )
        if out is None:
            counts[resource] = "error"
        else:
            lines = [ln for ln in out.split("\n") if ln.strip()]
            counts[resource] = len(lines)
    return counts


def _get_node_resources(cli):
    """Get node CPU/memory usage."""
    out = _run_cli(cli, ["top", "nodes", "--no-headers"], timeout=20)
    if not out:
        return "unavailable"
    nodes = []
    for line in out.strip().split("\n"):
        parts = line.split()
        if len(parts) >= 5:
            nodes.append(
                {
                    "name": parts[0],
                    "cpu": parts[1],
                    "cpu_pct": parts[2],
                    "mem": parts[3],
                    "mem_pct": parts[4],
                }
            )
    return nodes


def _take_snapshot(cli, snapshot_index, out_path):
    """Capture cluster diagnostics and append to file."""
    entry = {
        "index": snapshot_index,
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "gateway_restarts": _get_gateway_restarts(cli),
        "resource_counts": _get_resource_counts(cli),
        "node_resources": _get_node_resources(cli),
        "pods": {},
    }

    for namespace, prefix in _GATEWAY_PODS:
        pod_key = f"{namespace}/{prefix}"
        pod_name = _find_running_pod(cli, namespace, prefix)
        if not pod_name:
            entry["pods"][pod_key] = {"status": "not_found"}
            continue

        pod_data = {"pod_name": pod_name}
        for label, endpoint in _ENVOY_ENDPOINTS:
            raw = _exec_in_pod(cli, namespace, pod_name, endpoint)
            if label == "memory":
                try:
                    pod_data[label] = json.loads(raw)
                except (json.JSONDecodeError, TypeError):
                    pod_data[label] = raw
            else:
                pod_data[label] = raw
        entry["pods"][pod_key] = pod_data

    with open(out_path, "a") as f:
        f.write(json.dumps(entry) + "\n")

    logger.info(
        "Snapshot %d: restarts=%s resources=%s",
        snapshot_index,
        json.dumps(entry["gateway_restarts"], default=str),
        json.dumps(entry["resource_counts"], default=str),
    )


def _monitor_loop(cli, out_path, stop_event):
    """Background loop taking periodic snapshots."""
    index = 0
    try:
        _take_snapshot(cli, index, out_path)
    except Exception:
        logger.exception("Snapshot %d failed", index)
    while not stop_event.wait(_SNAPSHOT_INTERVAL):
        index += 1
        try:
            _take_snapshot(cli, index, out_path)
        except Exception:
            logger.exception("Snapshot %d failed", index)
    index += 1
    try:
        _take_snapshot(cli, index, out_path)
    except Exception:
        logger.exception("Final snapshot %d failed", index)


@pytest.fixture(scope="session", autouse=True)
def gateway_diagnostics_monitor(request):
    """Background monitor for Istio gateway proxy health.

    Periodically captures Envoy memory/stats from gateway pods,
    restart counts, resource counts, and node metrics.  Writes JSONL
    to $ARTIFACT_DIR/gateway_diagnostics.jsonl.
    Only active when --network-layer=gateway-api.
    """
    if not GATEWAY_PROXY_MEMORY:
        yield
        return

    network = request.config.getoption("--network-layer", default="istio")
    if network != "gateway-api":
        yield
        return

    # Run on controller (non-xdist) or first worker (gw0)
    # so exactly one monitor runs regardless of parallelism.
    try:
        worker = request.config.workerinput["workerid"]
    except (AttributeError, KeyError):
        worker = "master"
    if worker not in ("master", "gw0"):
        yield
        return

    cli = os.environ.get("KUBE_CLI", "oc")
    artifact_dir = os.environ.get("ARTIFACT_DIR", "/tmp")
    out_path = os.path.join(artifact_dir, "gateway_diagnostics.jsonl")
    stop_event = threading.Event()

    thread = threading.Thread(
        target=_monitor_loop,
        args=(cli, out_path, stop_event),
        daemon=True,
    )
    thread.start()
    logger.info(
        "Gateway diagnostics started "
        "(interval=%ds, cli=%s, output=%s)",
        _SNAPSHOT_INTERVAL,
        cli,
        out_path,
    )

    yield

    stop_event.set()
    thread.join(timeout=30)
    if thread.is_alive():
        logger.warning(
            "Monitor thread did not stop within 30s;"
            " final snapshot may be missing"
        )
