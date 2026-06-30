# Copyright 2024 The KServe Authors.
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

import json
import logging
import os
import subprocess
import threading
import time

import pytest

_envoy_logger = logging.getLogger("envoy_memory_monitor")

_TEST_NAMESPACE = "kserve-ci-e2e-test"

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

_SNAPSHOT_INTERVAL = 300


def _run_cli(cli, args, timeout=15):
    """Run a CLI command and return stdout, or error string."""
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
    except Exception:
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
    """Get node CPU/memory usage via kubectl top."""
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

    _envoy_logger.info(
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
        _envoy_logger.exception("Snapshot %d failed", index)
    while not stop_event.wait(_SNAPSHOT_INTERVAL):
        index += 1
        try:
            _take_snapshot(cli, index, out_path)
        except Exception:
            _envoy_logger.exception("Snapshot %d failed", index)
    index += 1
    try:
        _take_snapshot(cli, index, out_path)
    except Exception:
        _envoy_logger.exception("Final snapshot %d failed", index)


@pytest.fixture(scope="session", autouse=True)
def gateway_diagnostics_monitor(request):
    """Periodically capture gateway and cluster diagnostics.

    Every 5 minutes, captures: Envoy memory/stats from gateway pods,
    gateway pod restart counts, resource counts (HTTPRoute, LLMISvc,
    AuthPolicy), and node CPU/memory. Writes JSONL to
    $ARTIFACT_DIR/gateway_diagnostics.jsonl. Survives SIGKILL.
    Only active when --network-layer=gateway-api.
    """
    network = request.config.getoption("--network-layer", default="istio")
    if network != "gateway-api":
        yield
        return

    try:
        worker = request.config.workerinput["workerid"]
    except (AttributeError, KeyError):
        worker = "master"
    if worker not in ("master", "gw0"):
        yield
        return

    cli = os.environ.get("KUBE_CLI", "kubectl")
    artifact_dir = os.environ.get("ARTIFACT_DIR", "/tmp")
    out_path = os.path.join(artifact_dir, "gateway_diagnostics.jsonl")
    stop_event = threading.Event()

    thread = threading.Thread(
        target=_monitor_loop,
        args=(cli, out_path, stop_event),
        daemon=True,
    )
    thread.start()
    _envoy_logger.info(
        "Envoy memory monitor started (interval=%ds)",
        _SNAPSHOT_INTERVAL,
    )

    yield

    stop_event.set()
    thread.join(timeout=30)
